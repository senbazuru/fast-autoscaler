package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/sirupsen/logrus"
)

const (
	defScaleoutThreshold = 150
	defMinDesiredCount   = 5
	defCheckInterval     = 3
	checkGracePeriod     = 180

	prefixActiveConn = "Active connections:"
)

var (
	sigtermReceived int32

	config Config
	wg     sync.WaitGroup
)

// Config ... config persed config.json from ParameterStore
type Config struct {
	Services []Service `json:"Services"`
	Region   string
}

// Service ... autoscale settings each service
type Service struct {
	StatusURL         string `json:"StatusUrl"`
	StatusAuthName    string `json:"StatusAuthName"`
	StatusAuthValue   string `json:"StatusAuthValue"`
	ScaleoutThreshold int    `json:"ScaleoutThreshold"`
	MinDesiredCount   int64  `json:"MinDesiredCount"`
	CheckInterval     int    `json:"CheckInterval"`
	EcsClusterName    string `json:"EcsClusterName"`
	EcsServiceName    string `json:"EcsServiceName"`
	SlackWebhookURL   string `json:"SlackWebhookUrl"`
}

func init() {
	paramKey := os.Getenv("AUTOSCALER_PARAMKEY")
	if paramKey == "" {
		paramKey = "/ecs/fast-autoscaler/config.json"
	}
	config.Region = os.Getenv("AUTOSCALER_REGION")
	if config.Region == "" {
		config.Region = "ap-northeast-1"
	}
	configJSON := fetchParameterStore(paramKey)
	json.Unmarshal([]byte(configJSON), &config)
}

func main() {
	logger := &logrus.Logger{
		Out:       os.Stdout,
		Formatter: &logrus.JSONFormatter{},
		Level:     logrus.DebugLevel,
		Hooks:     make(logrus.LevelHooks),
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM)
	go func() {
		for {
			sig := <-c
			if sig == syscall.SIGTERM {
				logger.Infof("SIGTERM received. shutting down...")
				atomic.StoreInt32(&sigtermReceived, 1)
			}
		}
	}()

	if !config.validateParameter() {
		logger.Fatalln("invalid config json from ssm")
	}

	for _, s := range config.Services {
		wg.Add(1)
		go func(s Service) {
			logger := logger.WithFields(logrus.Fields{"service": s.EcsServiceName})
			startChecker(logger, s)
			defer wg.Done()
		}(s)
	}
	wg.Wait()
}

func startChecker(logger *logrus.Entry, s Service) {
	timerCh := make(chan bool)
	suspendCh := make(chan int)
	go checkLoop(logger, timerCh, suspendCh, s.CheckInterval)
	for {
		select {
		case <-timerCh: //タイマーイベント
			count, err := getNginxConns(s)
			if err != nil {
				logger.Warnf("getNginxConns err: %s", err)
			} else {
				logger.Infof("active conns: %d", count)
			}
			if s.ScaleoutThreshold < count {
				scaleout(logger, s, count)
				suspendCh <- checkGracePeriod
			}
		}
	}
}

func scaleout(logger *logrus.Entry, s Service, count int) {
	sess := session.Must(session.NewSession())
	svc := ecs.New(
		sess,
		aws.NewConfig().WithRegion(config.Region),
	)

	desiredCount, err := getDesiredCount(logger, svc, s)
	if err != nil {
		logger.Warnf("getDesiredCount error: %s", err)
		return
	}
	nextCount := desiredCount * 2
	if desiredCount < s.MinDesiredCount {
		nextCount = s.MinDesiredCount * 2
	}
	logger.Infof("change desired count current:%d, next:%d", desiredCount, nextCount)

	err = setDesiredCount(logger, svc, s, nextCount)
	if err != nil {
		logger.Warnf("setDesiredCount error: %s", err)
		return
	}

	scaleoutNotification(logger, s, count, int(desiredCount), int(nextCount))
}

func getDesiredCount(logger *logrus.Entry, svc *ecs.ECS, s Service) (int64, error) {
	resp, err := svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster: aws.String(s.EcsClusterName),
		Services: []*string{
			aws.String(s.EcsServiceName),
		},
	})
	if err != nil {
		return 0, fmt.Errorf("svc.DescribeServices error: %w", err)
	}
	return *resp.Services[0].DesiredCount, nil
}

func setDesiredCount(logger *logrus.Entry, svc *ecs.ECS, s Service, nextCount int64) error {
	_, err := svc.UpdateService(&ecs.UpdateServiceInput{
		Cluster:      aws.String(s.EcsClusterName),
		Service:      aws.String(s.EcsServiceName),
		DesiredCount: aws.Int64(nextCount),
	})
	if err != nil {
		return fmt.Errorf("svc.UpdateService error: %w", err)
	}
	return nil
}

//タイマーイベント
func checkLoop(logger *logrus.Entry, timerCh chan bool, suspendCh chan int, ticker int) {
	t := time.NewTicker(time.Duration(ticker) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C: //タイマーイベント
			if atomic.LoadInt32(&sigtermReceived) == 0 {
				timerCh <- true
			} else {
				logger.Infoln("stop timer")
				t.Stop()
			}
		case stopTime := <-suspendCh:
			logger.Infof("pause timer for %d seconds", stopTime)
			t.Stop()
			time.Sleep(time.Duration(stopTime) * time.Second)
			logger.Infoln("resume timer")
			t = time.NewTicker(time.Duration(ticker) * time.Second)
		}
	}
}

func getNginxConns(s Service) (count int, err error) {
	respBytes, err := requestNginxStatus(s)
	if err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(respBytes)))
	for scanner.Scan() {
		str := scanner.Text()
		idx := strings.Index(str, prefixActiveConn)
		if idx != -1 {
			countStr := strings.Replace(str[idx+len(prefixActiveConn):], " ", "", -1)
			count, err = strconv.Atoi(countStr)
			if err != nil {
				return 0, err
			}
			break
		}
	}
	return count, nil
}

func requestNginxStatus(s Service) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, s.StatusURL, nil)
	if s.StatusAuthName != "" {
		req.Header.Set(s.StatusAuthName, s.StatusAuthValue)
	}
	if err != nil {
		return nil, fmt.Errorf("http.NewRequest error: %w", err)
	}
	client := &http.Client{
		Timeout:   time.Duration(s.CheckInterval) * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpClient.Do error: %w", err)
	}
	defer resp.Body.Close()

	respStr, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return respStr, nil
}

func fetchParameterStore(paramName string) string {
	sess := session.Must(session.NewSession())
	svc := ssm.New(
		sess,
		aws.NewConfig().WithRegion(config.Region),
	)

	res, _ := svc.GetParameter(&ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	return *res.Parameter.Value
}

func (c *Config) validateParameter() bool {
	if len(c.Services) == 0 {
		return false
	}
	for idx := range c.Services {
		s := &c.Services[idx]
		if s.StatusURL == "" || s.EcsClusterName == "" || s.EcsServiceName == "" {
			return false
		}
		// 指定がない場合にデフォルト値で埋める
		s.ScaleoutThreshold = 10
		if s.ScaleoutThreshold == 0 {
			s.ScaleoutThreshold = defScaleoutThreshold
		}
		if s.MinDesiredCount == 0 {
			s.MinDesiredCount = defMinDesiredCount
		}
		if s.CheckInterval == 0 {
			s.CheckInterval = defCheckInterval
		}
	}
	return true
}

func scaleoutNotification(logger *logrus.Entry, s Service, activeConns, curCount, newCount int) {
	hookURL := s.SlackWebhookURL
	if hookURL == "" {
		logger.Infoln("slack notification skipped, because not configured")
		return
	}

	message := fmt.Sprintf("scaleout %s service", s.EcsServiceName)
	message = fmt.Sprintf(
		"%s\n```\nActiveConnections: %d\nDesiredCount(cur): %d\nDesiredCount(new): %d\n```",
		message,
		activeConns,
		curCount,
		newCount,
	)

	err := postToSlack(hookURL, "{\"text\":\""+message+"\"}")
	if err != nil {
		logger.Warnf("postToSlack error: %s", err)
	}
}

func postToSlack(hookURL string, msgJSON string) error {
	req, err := http.NewRequest(
		"POST",
		hookURL,
		bytes.NewBuffer([]byte(msgJSON)),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
