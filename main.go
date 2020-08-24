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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ssm"
)

const (
	defScaleoutThreshold = 150
	defMinDesiredCount   = 5
	defCheckInterval     = 3
	checkGracePeriod     = 180

	prefixActiveConn = "Active connections:"
)

var (
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
	if !config.validateParameter() {
		fmt.Println("invalid config json from ssm")
		os.Exit(0)
	}
	fmt.Println(config)

	for _, s := range config.Services {
		wg.Add(1)
		go func(s Service) {
			startChecker(s)
			defer wg.Done()
		}(s)
	}
	wg.Wait()
}

func startChecker(s Service) {
	timerCh := make(chan bool)
	suspendCh := make(chan int)
	go checkLoop(timerCh, suspendCh, s.CheckInterval)
	for {
		select {
		case <-timerCh: //タイマーイベント
			count, _ := getNginxConns(s)
			fmt.Printf("%s: %d\n", s.EcsServiceName, count)
			if s.ScaleoutThreshold < count {
				scaleout(s, count)
				suspendCh <- checkGracePeriod
			}
		}
	}
}

func scaleout(s Service, count int) {
	sess := session.Must(session.NewSession())
	svc := ecs.New(
		sess,
		aws.NewConfig().WithRegion(config.Region),
	)

	desiredCount, err := getDesiredCount(svc, s)
	if err != nil {
		fmt.Println(err)
		return
	}
	nextCount := desiredCount * 2
	if desiredCount < s.MinDesiredCount {
		nextCount = s.MinDesiredCount * 2
	}
	fmt.Printf("%s cur:%d, next:%d\n", s.EcsServiceName, desiredCount, nextCount)

	setDesiredCount(svc, s, nextCount)

	scaleoutNotification(s, count, int(desiredCount), int(nextCount))
}

func getDesiredCount(svc *ecs.ECS, s Service) (int64, error) {
	resp, err := svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster: aws.String(s.EcsClusterName),
		Services: []*string{
			aws.String(s.EcsServiceName),
		},
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case ecs.ErrCodeServerException:
				fmt.Println(ecs.ErrCodeServerException, aerr.Error())
			case ecs.ErrCodeClientException:
				fmt.Println(ecs.ErrCodeClientException, aerr.Error())
			case ecs.ErrCodeInvalidParameterException:
				fmt.Println(ecs.ErrCodeInvalidParameterException, aerr.Error())
			case ecs.ErrCodeClusterNotFoundException:
				fmt.Println(ecs.ErrCodeClusterNotFoundException, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
		}
		fmt.Println(err)
		return 0, err
	}
	desiredCount := *resp.Services[0].DesiredCount
	fmt.Println(desiredCount)
	return desiredCount, nil
}

func setDesiredCount(svc *ecs.ECS, s Service, nextCount int64) error {
	_, err := svc.UpdateService(&ecs.UpdateServiceInput{
		Cluster:      aws.String(s.EcsClusterName),
		Service:      aws.String(s.EcsServiceName),
		DesiredCount: aws.Int64(nextCount),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case ecs.ErrCodeServerException:
				fmt.Println(ecs.ErrCodeServerException, aerr.Error())
			case ecs.ErrCodeClientException:
				fmt.Println(ecs.ErrCodeClientException, aerr.Error())
			case ecs.ErrCodeInvalidParameterException:
				fmt.Println(ecs.ErrCodeInvalidParameterException, aerr.Error())
			case ecs.ErrCodeClusterNotFoundException:
				fmt.Println(ecs.ErrCodeClusterNotFoundException, aerr.Error())
			case ecs.ErrCodeServiceNotFoundException:
				fmt.Println(ecs.ErrCodeServiceNotFoundException, aerr.Error())
			case ecs.ErrCodeServiceNotActiveException:
				fmt.Println(ecs.ErrCodeServiceNotActiveException, aerr.Error())
			case ecs.ErrCodePlatformUnknownException:
				fmt.Println(ecs.ErrCodePlatformUnknownException, aerr.Error())
			case ecs.ErrCodePlatformTaskDefinitionIncompatibilityException:
				fmt.Println(ecs.ErrCodePlatformTaskDefinitionIncompatibilityException, aerr.Error())
			case ecs.ErrCodeAccessDeniedException:
				fmt.Println(ecs.ErrCodeAccessDeniedException, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
		}
	}
	return err
}

//タイマーイベント
func checkLoop(timerCh chan bool, suspendCh chan int, ticker int) {
	t := time.NewTicker(time.Duration(ticker) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C: //タイマーイベント
			timerCh <- true
		case stopTime := <-suspendCh:
			t.Stop()
			time.Sleep(time.Duration(stopTime) * time.Second)
			t = time.NewTicker(time.Duration(ticker) * time.Second)
		}
	}
}

func getNginxConns(s Service) (count int, err error) {
	respBytes, err := requestNginxStatus(s)
	if err != nil {
		fmt.Println(err)
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(string(respBytes)))
	for scanner.Scan() {
		str := scanner.Text()
		idx := strings.Index(str, prefixActiveConn)
		if idx != -1 {
			countStr := strings.Replace(str[idx+len(prefixActiveConn):], " ", "", -1)
			count, err = strconv.Atoi(countStr)
			if err != nil {
				return
			}
			break
		}
	}
	return
}

func requestNginxStatus(s Service) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, s.StatusURL, nil)
	if s.StatusAuthName != "" {
		req.Header.Set(s.StatusAuthName, s.StatusAuthValue)
	}
	if err != nil {
		err = fmt.Errorf("http.NewRequest error: %w", err)
		return nil, err
	}
	client := &http.Client{
		Timeout:   time.Duration(s.CheckInterval) * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("httpClient.Do error: %w", err)
		return nil, err
	}
	defer func() {
		_err := resp.Body.Close()
		if _err != nil {
			fmt.Printf("body.Close error: %s", _err)
		}
	}()

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

	res, err := svc.GetParameter(&ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		fmt.Println(err)
		return ""
	}
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

func scaleoutNotification(s Service, activeConns, curCount, newCount int) {
	hookURL := s.SlackWebhookURL
	if hookURL == "" {
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
		return
	}

	return
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
