# fast-autoscaler
AWS Fargate Fast Autoscaler - A container that Triggers your Fargate Autoscaling in Seconds(support multiple fargate)

## 概要

FargateでWebサービスを稼働させているサービスは、ターゲット追跡ポリシーなどのスケーリングポリシーを指定しているが  
マネージドな仕組みを使うとスケールアウトが遅いのでスケールアウトを高速に行うツールを開発した


### 処理フロー

![fastAutoscaler](https://raw.github.com/senbazuru/fast-autoscaler/master/fastAutoScaler.png)

## コード修正＆コンテナイメージ最新化手順

featureブランチ作成
masterブランチにマージ→latestタグでpublish
タグ(v0.1など)作成＆push → タグ名そのまま使ってpublish

## 利用手順

### Slack Webhook作成
### IAMロール作成
### 設定情報作成(toパラメータストア)
### ECS作成
