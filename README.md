# fast-autoscaler
AWS Fargate Fast Autoscaler - A container that Triggers your Fargate Autoscaling in Seconds(support multiple fargate)

## 概要

ALB+FargateでWebサービスを稼働させている場合に、ターゲット追跡ポリシーなどのスケーリングポリシーを  
使用してオートスケールする構成が一般的ですが、スケールアウト速度が十分でないため、スパイクアクセスに
耐えられないことがあります。  
fast-autoscalerは、そういうケースに対応し、より高速にスケールアウトさせるためのツールです。  

仕組みとしては、nginx-statusのアクティブ接続数を定期的に監視し、閾値を超えている場合にスケールアウト
するという単純なものですが、スパイクの予兆を検知できるため、スピーディにスケールアウトすることができます。  
もしスパイクではない誤検知であったとしても、マネージドなオートスケール機能と併用することにより自動的に
スケールインするためコスト的なデメリットは限定的です。  
また、１つのfast-autoscalerコンテナで複数ECSサービスを並行に処理できます。

## 前提

- fast-autoscalerはdockerコンテナイメージとしてghcr.ioから配布します
  - コンテナが動き、適切なAWSクレデンシャルを渡すことができればどこでも動きます  
    ただし、設定情報はパラメータストアから取得するため、Fargate等AWSマネージドサービスの利用を推奨します
- ALBの振り分け先には、nginxが稼働していてstub_statusが有効にしている必要があります
  - 以降、stub_statusのパスが`/nginx-status`である前提で記述しています

## 設定情報

### 環境変数

- パラメータストアのキー名：AUTOSCALER_PARAMKEY
  - デフォルト：/ecs/fast-autoscaler/config.json
- ECSサービス稼働リージョン：AUTOSCALER_REGION
  - デフォルト：ap-northeast-1

### 設定情報

- json形式でパラメータストアに保存
  - タイプはString or SecureStringどちらでもOK

```json:設定例
{
  "Services": [
    {
      "StatusUrl":"https://example-a.com/nginx-status",
      "StatusAuthName":"Status-Auth-Key",
      "StatusAuthValue":"hogefuga",
      "ScaleoutThreshold":100,
      "EcsClusterName":"example-app",
      "EcsServiceName":"example-app",
      "SlackWebhookUrl":"https://hooks.slack.com/services/XXXXXXXXX/XXXXXXXXX/xxxxxxxx"
    },
    {
      "StatusUrl":"https://example-b-1234567890.ap-northeast-1.elb.amazonaws.com/nginx-status",
      "ScaleoutThreshold":150,
      "MinDesiredCount":2,
      "CheckInterval":10,
      "EcsClusterName":"example-app-b",
      "EcsServiceName":"example-app-b"
    }
  ]
}
```

|フィールド名|説明|必須|デフォルト|例|
|---|---|---|---|---|
|StatusUrl|nginx-statusのURL|○||https://example-a.com/nginx-status|
|StatusAuthName|認証用HTTPヘッダ名|||Status-Auth-Key|
|StatusAuthValue|認証用HTTPヘッダ値|||hogefuga|
|ScaleoutThreshold|スケールアウト発動するActiveConnectionsの閾値||150|150|
|MinDesiredCount|スケールアウト時に現在のタスク数とみなす最小数||5|5|
|CheckInterval|nginx-statusをチェックする間隔(秒)||3|3|
|EcsClusterName|ECSクラスタ名|○||example-app|
|EcsServiceName|ECSサービス名|○||example-app|
|SlackWebhookUrl|Slack通知用WebhookURL|||https://hooks.slack.com/services/XXXXXXXXX/XXXXXXXXX/xxxxxxxx|

#### 補足

- StatusUrlには`nginxに到達できるドメイン＋stub_statusパス`を指定してください
  - verifyをスキップするので、SSL証明書のコモンネームとの不一致は許容されます(つまり、ALBドメイン名指定可能）
- スケールアウトは現在のタスク数を２倍にします
  - ただし、現在のタスク数が1の場合に2倍にしても2にしかならずスケール不足となる可能性があります  
    その場合はMinDesiredCountを指定してください。この値をタスク数の下限値として動作します  
    ```例）現在タスク数=３、MinDesiredCount=5の場合、3<5なので現在のタスク数を5とみなし、その2倍の10にDesiredCountを変更します。```
- StatusAuthName/StatusAuthValueで指定した値が`/nginx-status`へのリクエストにヘッダとして追加されます
  - fast-autoscaler以外から`/nginx-status`へのリクエストを防ぐためALB振り分けルールのヘッダ認証で使用します
- SlackWebhookUrlに指定があればスケールアウト時にSlackに通知を行います
  - 下記イメージ  
  scaleout example-app service  
```
ActiveConnections: 161
DesiredCount(cur): 2
DesiredCount(new): 10
```
  
## 処理フロー

1. コンテナ起動時にパラメータストアから設定情報(json形式,詳細後述)を取得
1. 指定された`/nginx-status`のURLにリクエストを送信し、Active Connectionsの値を確認
1. 閾値を超えていた場合)  
    -> スケールアウト実行 -> `/nginx-status`チェックを停止 -> 猶予期間経過後チェック再開  
1. 閾値を超えていない場合）  
    -> 一定周期での`/nginx-status`チェックを継続

下記イメージ  
![fastAutoscaler](https://raw.github.com/senbazuru/fast-autoscaler/master/fastAutoScaler.png)

## コンテナイメージ更新手順

fast-autoscalerの更新手順

1. featureブランチ作成
1. コード修正
1. PR作成
1. masterブランチにマージ  
   -> latestタグでpublishされます
1. タグ(v0.1など)作成＆push  
   -> gitタグ名をコンテナイメージのタグとしてpublish

## 利用手順

### Slack Webhook作成

[こちら](https://slack.com/help/articles/115005265063-Incoming-webhooks-for-Slack)に従いWebhookを発行する

### IAMロール作成

Fargateのタスクロールに指定するIAMロールを作成します。  
下記ポリシーを含むロールを作成してください。
```json:fast-autoscaler-task-role
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "VisualEditor0",
            "Effect": "Allow",
            "Action": [
                "ecs:UpdateService",
                "ecs:DescribeServices",
                "ssm:GetParameter" 
            ],
            "Resource": "*" 
        }
    ]
}
```

### 設定情報作成(toパラメータストア)

上述の設定情報をパラメータストアに作成します。  
パラメータキー：/ecs/fast-autoscaler/config.json

### ECS作成

- タスク定義作成
  - タスク定義名：fast-autoscaler
  - コンテナイメージURLに下記を指定  
    ghcr.io/senbazuru/fast-autoscaler:latest
  - CPU/Memoryの割り当ては最小値でOK
  - タスク数は1でOK
- ECSクラスタ／サービス作成
  - クラスタ名：fast-autoscaler
  - サービス名：fast-autoscaler
    - タスク数(1)で実行
