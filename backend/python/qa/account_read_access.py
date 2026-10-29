"""Live, read-only descendant grants and bounded child-reference discovery."""
import copy
from dataclasses import dataclass
import os

from boto3.dynamodb.conditions import Key

from source_revision import IDENTIFIER
from tool_history import fingerprint

FLAG = 'ACCOUNT_DESCENDANT_READS_ENABLED'
MAX_ANCESTORS = 64
MAX_READABLE_ACCOUNTS = 1000
MAX_DISCOVERY_PAGES = 10000
MEMBER_FIELDS = ('PK', 'SK', 'entityType', 'accountId', 'userId', 'role', 'addedAt',
                 'summaryPrivacyVersion')
META_FIELDS = ('PK', 'SK', 'entityType', 'accountId', 'parentAccountId')


def valid_id(value):
    return type(value) is str and IDENTIFIER.fullmatch(value) is not None


def projection(fields):
    names = {f'#a{i}': name for i, name in enumerate(fields)}
    return {'ProjectionExpression': ', '.join(names), 'ExpressionAttributeNames': names}


class _MalformedInheritedMember(ValueError):
    """An existing canonical-key row cannot be treated as missing membership."""


@dataclass(frozen=True)
class AccountReadGrant:
    account_id: str
    granting_account_id: str
    member: dict
    path: tuple = ()

    @property
    def inherited(self):
        return self.account_id != self.granting_account_id

    @property
    def permission(self):
        return 'read' if self.inherited else 'edit'

    def privacy_member(self):
        member = copy.deepcopy(self.member)
        if self.inherited:
            member['summaryPrivacyVersion'] = 1
        return member

    def fingerprint(self):
        return fingerprint(['account-read-grant-v1', self.account_id, self.granting_account_id,
                            self.path_as_list(), self.member])

    def path_as_list(self):
        return [list(link) for link in self.path]


class AccountReadAccess:
    """No positive grant cache: every resolve/recheck strongly reads live rows."""
    def __init__(self, table, query_all=None):
        self.table = table
        self.query_all = query_all

    def _get(self, account_id, sk, fields):
        item = self.table.get_item(
            Key={'PK': 'ACCOUNT#' + account_id, 'SK': sk}, ConsistentRead=True,
            **projection(fields),
        ).get('Item')
        if item is not None and type(item) is not dict:
            raise ValueError('Invalid Account authorization response')
        return item

    def _member(self, account_id, user_id, *, strict=False):
        member = self._get(account_id, 'MEMBER#' + user_id, MEMBER_FIELDS)
        if member is None:
            return None
        # Preserve the preexisting direct-grant contract. New inherited grants
        # additionally require the canonical storage and entity identities.
        if strict and (member.get('PK') != 'ACCOUNT#' + account_id
                       or member.get('SK') != 'MEMBER#' + user_id
                       or member.get('accountId') != account_id
                       or member.get('userId') != user_id
                       or member.get('entityType') != 'ACCOUNT_MEMBER'
                       or type(member.get('role')) is not str or not member['role']):
            raise _MalformedInheritedMember('Invalid canonical inherited membership')
        if not member or member.get('accountId') != account_id or member.get('userId') != user_id:
            return None
        return member

    def _meta(self, account_id):
        row = self._get(account_id, 'META', META_FIELDS)
        if (not row or row.get('PK') != 'ACCOUNT#' + account_id or row.get('SK') != 'META'
                or row.get('accountId') != account_id or row.get('entityType') != 'ACCOUNT'):
            return None
        parent = row.get('parentAccountId', '')
        if parent != '' and not valid_id(parent):
            return None
        return row

    def resolve(self, account_id, user_id):
        if not valid_id(account_id) or not valid_id(user_id):
            return None
        direct = self._member(account_id, user_id)
        if direct:
            return AccountReadGrant(account_id, account_id, direct)
        if os.environ.get(FLAG, '') != '1':
            return None
        path, seen, selected = [], set(), None
        current = account_id
        while current:
            if current in seen or len(path) > MAX_ANCESTORS:
                return None
            seen.add(current)
            meta = self._meta(current)
            if meta is None:
                return None
            parent = meta.get('parentAccountId', '')
            path.append((current, parent))
            if current != account_id and selected is None:
                try:
                    member = self._member(current, user_id, strict=True)
                except _MalformedInheritedMember:
                    return None
                if member:
                    selected = (current, member)
            current = parent
        if selected is None:
            return None
        return AccountReadGrant(account_id, selected[0], selected[1], tuple(path))

    def revalidate(self, grant, user_id):
        current = self.resolve(grant.account_id, user_id)
        return current is not None and current.fingerprint() == grant.fingerprint()

    def _query(self, pk, prefix, fields, *, index=False):
        partition, sort = ('GSI1PK', 'GSI1SK') if index else ('PK', 'SK')
        kwargs = {'KeyConditionExpression': Key(partition).eq(pk) & Key(sort).begins_with(prefix),
                  **projection(fields)}
        if index:
            kwargs['IndexName'] = 'GSI1'
        else:
            kwargs['ConsistentRead'] = True
        if self.query_all is not None:
            yield from self.query_all(**kwargs)
            return
        kwargs.update(TableName=self.table.name, Limit=100)
        paginator = self.table.meta.client.get_paginator('query')
        seen_tokens = set()
        for page_number, page in enumerate(paginator.paginate(**kwargs), 1):
            if page_number > MAX_DISCOVERY_PAGES:
                raise ValueError('Account discovery page limit exceeded')
            rows = page.get('Items')
            if type(rows) is not list or any(type(row) is not dict for row in rows):
                raise ValueError('Invalid Account discovery page')
            yield from rows
            token = page.get('LastEvaluatedKey')
            if token:
                if type(token) is not dict:
                    raise ValueError('Invalid Account discovery cursor')
                token_hash = fingerprint(token)
                if token_hash in seen_tokens:
                    raise ValueError('Account discovery cursor did not advance')
                seen_tokens.add(token_hash)

    def discover(self, user_id, seed_ids=None):
        if not valid_id(user_id):
            raise ValueError('Current authenticated user is required')
        if seed_ids is None:
            seed_ids = [row.get('accountId') for row in self._query(
                'USER#' + user_id, 'ACCOUNT#', ('accountId',), index=True)]
        readable = {}
        for account_id in sorted({value for value in seed_ids if valid_id(value)}):
            grant = self.resolve(account_id, user_id)
            if grant is not None:
                readable[account_id] = grant
                if len(readable) > MAX_READABLE_ACCOUNTS:
                    raise ValueError('Readable Account limit exceeded (1000)')
        if os.environ.get(FLAG, '') == '1':
            pending = list(readable)
            for parent in pending:
                for ref in self._query('ACCOUNT#' + parent, 'CHILDREF#',
                                       ('PK', 'SK', 'accountId', 'parentAccountId', 'entityType')):
                    child = ref.get('accountId')
                    if (not valid_id(child) or ref.get('PK') != 'ACCOUNT#' + parent
                            or ref.get('SK') != 'CHILDREF#' + child
                            or ref.get('parentAccountId') != parent
                            or ref.get('entityType') != 'ACCOUNT_CHILD_REF'):
                        continue
                    meta = self._meta(child)
                    if meta is None or meta.get('parentAccountId') != parent:
                        continue
                    grant = self.resolve(child, user_id)
                    if grant is None:
                        continue
                    if child not in readable:
                        readable[child] = grant
                        pending.append(child)
                        if len(readable) > MAX_READABLE_ACCOUNTS:
                            raise ValueError('Readable Account limit exceeded (1000)')
        for grant in readable.values():
            if not self.revalidate(grant, user_id):
                raise PermissionError('Account read grant changed during discovery')
        return readable
