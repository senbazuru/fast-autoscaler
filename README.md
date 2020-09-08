# fast-autoscaler
AWS Fargate Fast Autoscaler - A container that Triggers your Fargate Autoscaling in Seconds(support multiple fargate)

## 概要

ALB+FargateでWebサービスを稼働させている場合に、ターゲット追跡ポリシーなどのスケーリングポリシーを使用して  
オートスケールする構成が一般的です。  
ただ、マネージドな仕組みではスケールアウト速度が十分でないため、スパイクアクセスに耐えられないことがあります。  
fast-autoscalerは、そういうケースに対応し、より高速にスケールアウトさせるためのツールです。 

１つのfast-autoscalerコンテナで複数ECSサービスを並行に処理できます。

## 前提

- fast-autoscalerはdockerコンテナイメージとしてghcr.ioから配布します
  - dockerが動き、適切なAWSクレデンシャルを渡すことができればどこでも動きます  
    ただし、設定情報はパラメータストアから取得するため、Fargate等AWSマネージドサービスの利用推奨
- ALBの振り分け先には、nginxが稼働していてstub_statusが有効になっている
  - 以降、stub_statusのパスが`/nginx-status`である前提

## 処理フロー

1. コンテナ起動時にパラメータストアから設定情報(json形式,詳細後述)を取得
1. 指定された`/nginx-status`のURLにリクエストを送信し、Active Connectionsの値を確認
1. 閾値を超えていた場合)  
    -> スケールアウト実行 -> `/nginx-status`チェックを停止 -> 猶予期間経過後チェック再開  
1. 閾値を超えていない場合）  
    -> 一定周期での`/nginx-status`チェックを継続

下記イメージ
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
