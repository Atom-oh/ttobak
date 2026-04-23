import json
import os
import logging
import boto3
from datetime import datetime

logger = logging.getLogger()
logger.setLevel(logging.INFO)

TABLE_NAME = os.environ.get('TABLE_NAME', 'ttobak-main')
KB_BUCKET = os.environ.get('KB_BUCKET_NAME', '')

s3 = boto3.client('s3')
dynamodb = boto3.resource('dynamodb')
table = dynamodb.Table(TABLE_NAME)


def handler(event, context):
    action = event.get('actionGroup', '')
    function_name = event.get('function', '')
    parameters = {p['name']: p['value'] for p in event.get('parameters', [])}

    research_id = parameters.get('researchId', '')
    content = parameters.get('content', '')
    summary = parameters.get('summary', '')
    source_count = int(parameters.get('sourceCount', '0'))
    word_count = int(parameters.get('wordCount', '0'))

    if not research_id or not content:
        return action_response(event, 'error', 'researchId and content are required')

    s3_key = f'shared/research/{research_id}.md'

    s3.put_object(
        Bucket=KB_BUCKET,
        Key=s3_key,
        Body=content.encode('utf-8'),
        ContentType='text/markdown; charset=utf-8',
    )
    logger.info(f'Saved report to s3://{KB_BUCKET}/{s3_key}')

    table.update_item(
        Key={'PK': f'RESEARCH#{research_id}', 'SK': 'CONFIG'},
        UpdateExpression='SET #s = :s, completedAt = :c, s3Key = :k, sourceCount = :sc, wordCount = :wc, summary = :sm',
        ExpressionAttributeNames={'#s': 'status'},
        ExpressionAttributeValues={
            ':s': 'done',
            ':c': datetime.utcnow().isoformat() + 'Z',
            ':k': s3_key,
            ':sc': source_count,
            ':wc': word_count,
            ':sm': summary[:1000],
        },
    )

    return action_response(event, 'saved', f's3://{KB_BUCKET}/{s3_key}')


def action_response(event, status, message):
    return {
        'messageVersion': '1.0',
        'response': {
            'actionGroup': event.get('actionGroup', ''),
            'function': event.get('function', ''),
            'functionResponse': {
                'responseBody': {
                    'TEXT': {
                        'body': json.dumps({'status': status, 'message': message}),
                    }
                }
            }
        }
    }
