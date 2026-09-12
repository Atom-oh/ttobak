"""Fresh, complete account views for readonly tool history; no AWS client setup."""
from boto3.dynamodb.conditions import Key

from source_revision import IDENTIFIER
from tool_history import CompleteRead, normalize_input

MEMBER_FIELDS = ('PK', 'SK', 'accountId', 'userId', 'role')
ACCOUNT_FIELDS = ('PK', 'SK', 'accountId', 'name', 'aliases', 'industry')
MEETING_AUTH = ('PK', 'SK', 'meetingId', 'userId', 'accountId', 'sharedToAccount')
RESEARCH_AUTH = ('PK', 'SK', 'researchId', 'userId', 'accountIds', 'trashedAt')


def _identifier(value):
    return type(value) is str and IDENTIFIER.fullmatch(value) is not None


def _text(item, key, default=''):
    value = item.get(key, default)
    if value is None:
        return default
    if type(value) is not str:
        raise ValueError('Invalid account-view text field')
    return value


def _strings(value):
    if value is None:
        return []
    if type(value) not in (list, set) or any(type(item) is not str for item in value):
        raise ValueError('Invalid account-view string collection')
    return sorted(value) if type(value) is set else list(value)


def _projection(fields):
    names = {f'#p{i}': name for i, name in enumerate(fields)}
    return {'ProjectionExpression': ', '.join(names), 'ExpressionAttributeNames': names}


class StrictAccountReader:
    """Only strong authorized views are eligible for CompleteRead attestation."""
    def __init__(self, table):
        self.table = table

    @staticmethod
    def _user(user_id):
        if not _identifier(user_id):
            raise ValueError('Current authenticated user is required')

    def _get(self, pk, sk, fields):
        response = self.table.get_item(Key={'PK': pk, 'SK': sk}, ConsistentRead=True,
                                       **_projection(fields))
        item = response.get('Item')
        if item is None:
            return None
        if type(item) is not dict or item.get('PK') != pk or item.get('SK') != sk:
            raise ValueError('Canonical account-view identity mismatch')
        return item

    def _query(self, pk, prefix, fields, *, index=False):
        partition, sort = ('GSI1PK', 'GSI1SK') if index else ('PK', 'SK')
        kwargs = {
            'TableName': self.table.name,
            'KeyConditionExpression': Key(partition).eq(pk) & Key(sort).begins_with(prefix),
            'Limit': 100,
            **_projection(fields),
        }
        if index:
            kwargs['IndexName'] = 'GSI1'
        else:
            kwargs.update(ConsistentRead=True, ScanIndexForward=False)
        paginator = self.table.meta.client.get_paginator('query')
        for page in paginator.paginate(**kwargs):
            items = page.get('Items')
            if type(items) is not list:
                raise ValueError('Invalid account-view query response')
            for item in items:
                if type(item) is not dict:
                    raise ValueError('Invalid account-view query item')
                yield item

    def _member(self, account_id, user_id):
        row = self._get('ACCOUNT#' + account_id, 'MEMBER#' + user_id, MEMBER_FIELDS)
        if row is None or row.get('accountId') != account_id or row.get('userId') != user_id:
            return None
        _text(row, 'role')
        return row

    def _recheck_member(self, user_id, before):
        if self._member(before['accountId'], user_id) != before:
            raise PermissionError('Account membership changed; retry with current access')

    def _accounts(self, user_id):
        self._user(user_id)
        ids = set()
        for candidate in self._query(
                'USER#' + user_id, 'ACCOUNT#',
                ('PK', 'SK', 'accountId', 'userId', 'GSI1PK', 'GSI1SK'), index=True):
            account_id = candidate.get('accountId')
            if (not _identifier(account_id) or candidate.get('PK') != 'ACCOUNT#' + account_id
                    or candidate.get('SK') != 'MEMBER#' + user_id
                    or candidate.get('userId') != user_id
                    or candidate.get('GSI1PK') != 'USER#' + user_id
                    or candidate.get('GSI1SK') != 'ACCOUNT#' + account_id):
                continue
            ids.add(account_id)
        accounts = []
        for account_id in sorted(ids):
            member = self._member(account_id, user_id)
            if member is None:
                continue
            meta = self._get('ACCOUNT#' + account_id, 'META', ACCOUNT_FIELDS)
            if meta is None:
                continue
            if meta.get('accountId') != account_id:
                raise ValueError('Canonical account ID mismatch')
            _text(meta, 'name')
            _strings(meta.get('aliases'))
            accounts.append((meta, member))
        return accounts

    def _resolve(self, user_id, query):
        normalized = query.strip().casefold()
        matches = [(account, member) for account, member in self._accounts(user_id)
                   if normalized in {_text(account, 'accountId').casefold(),
                                     _text(account, 'name').strip().casefold(),
                                     *(alias.strip().casefold() for alias in _strings(account.get('aliases')))}]
        if len(matches) != 1:
            raise LookupError('Account is unavailable or ambiguous for the current user')
        return matches[0]

    def list_accounts(self, user_id):
        accounts = self._accounts(user_id)
        result = [{'accountId': account['accountId'], 'name': _text(account, 'name'),
                   'role': _text(member, 'role')} for account, member in accounts]
        for _, member in accounts:
            self._recheck_member(user_id, member)
        return CompleteRead(sorted(result, key=lambda row: (row['name'].casefold(), row['accountId'])))

    def _insights(self, account_id, date_from=None, date_to=None, types=None):
        result = []
        for row in self._query('ACCOUNT#' + account_id, 'INSIGHT#',
                               ('PK', 'SK', 'insightId', 'type', 'text', 'occurredAt', 'sourceType', 'entities')):
            if row.get('PK') != 'ACCOUNT#' + account_id or not row.get('SK', '').startswith('INSIGHT#'):
                raise ValueError('Insight escaped the authorized account')
            occurred = _text(row, 'occurredAt')
            kind = _text(row, 'type')
            if ((date_from and occurred and occurred < date_from)
                    or (date_to and occurred and occurred > date_to)
                    or (types and kind not in types)):
                continue
            result.append({'insightId': _text(row, 'insightId') or row['SK'],
                           'type': kind, 'text': _text(row, 'text'), 'occurredAt': occurred,
                           'sourceType': _text(row, 'sourceType'), 'entities': _strings(row.get('entities'))})
        return sorted(result, key=lambda item: (item['occurredAt'], item['insightId']), reverse=True)

    def get_account_insights(self, user_id, account_query, date_from=None, date_to=None, types=None):
        self._user(user_id)
        args = normalize_input('get_account_insights',
                               {'account': account_query, 'from': date_from, 'to': date_to, 'types': types})
        account, member = self._resolve(user_id, args['account'])
        insights = self._insights(account['accountId'], args.get('from'), args.get('to'), args.get('types'))
        self._recheck_member(user_id, member)
        return CompleteRead({'accountId': account['accountId'], 'account': _text(account, 'name'), 'insights': insights})

    @staticmethod
    def _published(row, owner, meeting_id, account_id):
        return (row is not None and row.get('meetingId') == meeting_id and row.get('userId') == owner
                and row.get('accountId') == account_id and row.get('sharedToAccount') is True)

    def _meetings(self, account_id):
        result, guards, seen = [], [], set()
        for ref in self._query('ACCOUNT#' + account_id, 'MEETINGREF#',
                               ('PK', 'SK', 'accountId', 'meetingId', 'ownerUserId')):
            meeting_id, owner = ref.get('meetingId'), ref.get('ownerUserId')
            if (ref.get('PK') != 'ACCOUNT#' + account_id or ref.get('accountId') != account_id
                    or not ref.get('SK', '').startswith('MEETINGREF#')
                    or not _identifier(meeting_id) or not _identifier(owner) or (owner, meeting_id) in seen):
                continue
            seen.add((owner, meeting_id))
            pk, sk = 'USER#' + owner, 'MEETING#' + meeting_id
            auth = self._get(pk, sk, MEETING_AUTH)
            if not self._published(auth, owner, meeting_id, account_id):
                continue
            current = self._get(pk, sk, MEETING_AUTH + ('title', 'date', 'createdAt'))
            if not self._published(current, owner, meeting_id, account_id):
                raise PermissionError('Meeting account publication changed during read')
            result.append({'meetingId': meeting_id, 'title': _text(current, 'title'),
                           'date': _text(current, 'date') or _text(current, 'createdAt')})
            guards.append((pk, sk, owner, meeting_id))
        return sorted(result, key=lambda row: (row['date'], row['meetingId']), reverse=True), guards

    @staticmethod
    def _linked(row, research_id, account_id):
        return (row is not None and row.get('researchId') == research_id and _identifier(row.get('userId'))
                and not row.get('trashedAt') and account_id in _strings(row.get('accountIds')))

    def _research(self, account_id):
        result, guards, seen = [], [], set()
        for ref in self._query('ACCOUNT#' + account_id, 'RESEARCHREF#',
                               ('PK', 'SK', 'accountId', 'researchId')):
            research_id = ref.get('researchId')
            if (ref.get('PK') != 'ACCOUNT#' + account_id or ref.get('accountId') != account_id
                    or not _identifier(research_id) or ref.get('SK') != 'RESEARCHREF#' + research_id
                    or research_id in seen):
                continue
            seen.add(research_id)
            pk = 'RESEARCH#' + research_id
            auth = self._get(pk, 'CONFIG', RESEARCH_AUTH)
            if not self._linked(auth, research_id, account_id):
                continue
            current = self._get(pk, 'CONFIG', RESEARCH_AUTH + ('topic', 'summary', 'status'))
            if not self._linked(current, research_id, account_id):
                raise PermissionError('Research account linkage changed during read')
            result.append({'researchId': research_id, 'topic': _text(current, 'topic'),
                           'summary': _text(current, 'summary')[:200], 'status': _text(current, 'status')})
            guards.append(research_id)
        return sorted(result, key=lambda row: row['researchId']), guards

    def get_account_brief(self, user_id, account_query):
        self._user(user_id)
        args = normalize_input('get_account_brief', {'account': account_query})
        account, member = self._resolve(user_id, args['account'])
        account_id = account['accountId']
        insights = self._insights(account_id)
        meetings, meeting_guards = self._meetings(account_id)
        research, research_guards = self._research(account_id)
        for pk, sk, owner, meeting_id in meeting_guards:
            if not self._published(self._get(pk, sk, MEETING_AUTH), owner, meeting_id, account_id):
                raise PermissionError('Meeting account publication changed during aggregation')
        for research_id in research_guards:
            if not self._linked(self._get('RESEARCH#' + research_id, 'CONFIG', RESEARCH_AUTH), research_id, account_id):
                raise PermissionError('Research account linkage changed during aggregation')
        self._recheck_member(user_id, member)
        by_type = {}
        for insight in insights:
            by_type.setdefault(insight['type'], []).append(insight)
        return CompleteRead({'accountId': account_id, 'account': _text(account, 'name'),
                             'industry': _text(account, 'industry'), 'insightsByType': by_type,
                             'meetings': meetings, 'research': research})
