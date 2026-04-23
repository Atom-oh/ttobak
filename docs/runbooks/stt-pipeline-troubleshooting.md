# STT Pipeline Troubleshooting Runbook

## Symptoms

### Meeting stuck in "transcribing" status
1. Check transcribe Lambda logs:
   ```bash
   aws logs tail /aws/lambda/ttobak-transcribe --since 30m --region ap-northeast-2
   ```
2. If `sttProvider=whisper`, check ECS task status:
   ```bash
   aws ecs list-tasks --cluster ttobak-whisper --region ap-northeast-2
   aws ecs describe-tasks --cluster ttobak-whisper --tasks <TASK_ARN> --region ap-northeast-2
   ```
3. If ECS task failed, check CloudWatch logs:
   ```bash
   aws logs tail /ecs/whisper --since 1h --region ap-northeast-2
   ```
4. Auto-expiry: GetMeeting handler marks stuck status as `error` after 30 minutes.

### ECS task not starting (zero-scale cold start)
1. Check ASG desired capacity:
   ```bash
   aws autoscaling describe-auto-scaling-groups --auto-scaling-group-names ttobak-whisper-asg --region ap-northeast-2 --query 'AutoScalingGroups[0].{Min:MinSize,Max:MaxSize,Desired:DesiredCapacity}'
   ```
2. Check Capacity Provider:
   ```bash
   aws ecs describe-capacity-providers --capacity-providers ttobak-whisper-spot --region ap-northeast-2
   ```
3. Check for Spot capacity issues:
   ```bash
   aws ec2 describe-spot-instance-requests --filters "Name=state,Values=open,active" --region ap-northeast-2
   ```

### Transcribe output not triggering summarize
1. Verify transcript was uploaded to S3:
   ```bash
   aws s3 ls s3://ttobak-assets-<ACCOUNT>/transcripts/<MEETING_ID>.json --region ap-northeast-2
   ```
2. Check EventBridge rule:
   ```bash
   aws events describe-rule --name ttobak-transcript-upload --region ap-northeast-2
   ```

## Recovery

### Force retry a meeting
```bash
aws events put-events --entries '[{"Source":"aws.s3","DetailType":"Object Created","Detail":"{\"bucket\":{\"name\":\"ttobak-assets-<ACCOUNT>\"},\"object\":{\"key\":\"audio/<USER_ID>/<MEETING_ID>/<FILENAME>\"}}"}]' --region ap-northeast-2
```

### Reset meeting status
```bash
aws dynamodb update-item --table-name ttobak-main \
  --key '{"PK":{"S":"USER#<USER_ID>"},"SK":{"S":"MEETING#<MEETING_ID>"}}' \
  --update-expression "SET #s = :s" \
  --expression-attribute-names '{"#s":"status"}' \
  --expression-attribute-values '{":s":{"S":"uploaded"}}' \
  --region ap-northeast-2
```
