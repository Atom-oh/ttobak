#!/usr/bin/env python3
"""Fix crawler sources with incorrect newsQueries values.

The newsQueries field was populated with news outlet names (e.g., "Naver News",
"ZDNet Korea") instead of interest keywords. This script clears those bad values
so the crawler falls back to default keywords ("IT", "클라우드", "AI", "디지털전환").

Also fixes subscription records (USER#xxx / CRAWLSUB#xxx).

Usage:
  python3 scripts/fix-crawler-newsqueries.py          # dry run
  python3 scripts/fix-crawler-newsqueries.py --apply   # apply changes
"""

import sys
import boto3

TABLE_NAME = 'ttobak-main'
DRY_RUN = '--apply' not in sys.argv

KNOWN_OUTLET_NAMES = {
    'google', 'naver', 'google news', 'naver news', 'zdnet korea',
    'it chosun', 'bloter', 'etnews', 'byline', 'techm', 'aitimes',
}

dynamodb = boto3.resource('dynamodb', region_name='ap-northeast-2')
table = dynamodb.Table(TABLE_NAME)


def is_bad_keyword(kw: str) -> bool:
    return kw.lower() in KNOWN_OUTLET_NAMES


def _scan_all(**kwargs):
    items = []
    while True:
        resp = table.scan(**kwargs)
        items.extend(resp.get('Items', []))
        if 'LastEvaluatedKey' not in resp:
            break
        kwargs['ExclusiveStartKey'] = resp['LastEvaluatedKey']
    return items


def fix_source_configs():
    print('=== Fixing CRAWLER# source configs ===')
    items = _scan_all(
        FilterExpression='begins_with(PK, :pk) AND SK = :sk',
        ExpressionAttributeValues={':pk': 'CRAWLER#', ':sk': 'CONFIG'},
    )
    for item in items:
        pk = item['PK']
        source_name = item.get('sourceName', '')
        old_queries = item.get('newsQueries', [])
        if not old_queries:
            continue

        bad = [q for q in old_queries if is_bad_keyword(q)]
        good = [q for q in old_queries if not is_bad_keyword(q)]

        if not bad:
            print(f'  {pk}: OK (queries={old_queries})')
            continue

        print(f'  {pk} ({source_name}):')
        print(f'    BEFORE: {old_queries}')
        print(f'    AFTER:  {good if good else "(empty → will use defaults)"}')
        print(f'    REMOVED: {bad}')

        if not DRY_RUN:
            table.update_item(
                Key={'PK': pk, 'SK': 'CONFIG'},
                UpdateExpression='SET newsQueries = :q',
                ExpressionAttributeValues={':q': good},
            )
            print('    ✓ Updated')


def fix_subscriptions():
    print('\n=== Fixing USER# subscription records ===')
    items = _scan_all(
        FilterExpression='begins_with(PK, :pk) AND begins_with(SK, :sk)',
        ExpressionAttributeValues={':pk': 'USER#', ':sk': 'CRAWLSUB#'},
    )
    for item in items:
        pk = item['PK']
        sk = item['SK']
        old_queries = item.get('newsQueries', [])
        old_sources = item.get('newsSources', [])
        if not old_queries and not old_sources:
            continue

        bad_queries = [q for q in old_queries if is_bad_keyword(q)]
        good_queries = [q for q in old_queries if not is_bad_keyword(q)]
        bad_sources = [s for s in old_sources if is_bad_keyword(s)]
        good_sources = [s for s in old_sources if not is_bad_keyword(s)]

        if not bad_queries and not bad_sources:
            continue

        print(f'  {pk} / {sk}:')
        if bad_queries:
            print(f'    newsQueries: {old_queries} → {good_queries}')
        if bad_sources:
            print(f'    newsSources: {old_sources} → {good_sources}')

        if not DRY_RUN:
            table.update_item(
                Key={'PK': pk, 'SK': sk},
                UpdateExpression='SET newsQueries = :q, newsSources = :s',
                ExpressionAttributeValues={':q': good_queries, ':s': good_sources},
            )
            print('    ✓ Updated')


if __name__ == '__main__':
    if DRY_RUN:
        print('DRY RUN — pass --apply to write changes\n')
    else:
        print('APPLYING CHANGES\n')

    fix_source_configs()
    fix_subscriptions()

    if DRY_RUN:
        print('\n(dry run complete, no changes made)')
