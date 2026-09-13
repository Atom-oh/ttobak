import copy
import unittest

from document_context import read_current_document


class MemoryTable:
    def __init__(self):
        self.rows = {}
        self.calls = []

    def get_item(self, **kwargs):
        self.calls.append(kwargs)
        if not kwargs.get('ConsistentRead'):
            raise AssertionError('authorization/content reads must be current')
        key = kwargs['Key']
        item = self.rows.get((key['PK'], key['SK']))
        return {'Item': copy.deepcopy(item)} if item else {}


class DocumentContextTests(unittest.TestCase):
    def setUp(self):
        self.table = MemoryTable()
        self.personal = {
            'PK': 'USER#owner', 'SK': 'DOC#doc', 'docId': 'doc',
            'entityType': 'USER_DOC', 'sourceUserId': 'owner',
            'title': '합성 문서', 'content': '현재 메모', 'updatedAt': 'v1',
            'publicShareToken': 'must-not-be-returned',
        }
        self.table.rows[('USER#owner', 'DOC#doc')] = self.personal

    def read(self, user='owner', pk='USER#owner'):
        return read_current_document(self.table, user, pk, 'doc')

    def test_owner_reads_current_text_without_public_token(self):
        first = self.read()
        self.assertEqual(first['content'], '현재 메모')
        self.assertNotIn('publicShareToken', first)
        self.personal['content'] = '수정된 메모'
        self.assertEqual(self.read()['content'], '수정된 메모')

    def test_reference_share_rechecks_grant_and_source_deletion(self):
        self.assertIsNone(self.read('reader'))
        grant = {'entityType': 'DOC_SHARE', 'meetingId': 'doc',
                 'ownerId': 'owner', 'sharedToId': 'reader', 'permission': 'read'}
        self.table.rows[('USER#reader', 'SHAREDDOC#doc')] = grant
        self.assertEqual(self.read('reader')['content'], '현재 메모')
        del self.table.rows[('USER#reader', 'SHAREDDOC#doc')]
        self.assertIsNone(self.read('reader'))
        self.table.rows[('USER#reader', 'SHAREDDOC#doc')] = grant
        del self.table.rows[('USER#owner', 'DOC#doc')]
        self.assertIsNone(self.read('reader'))

    def test_wrong_share_identity_and_public_token_never_grant_access(self):
        self.assertIsNone(self.read('reader'))
        for field, bad in [('entityType', 'SHARE'), ('meetingId', 'other'),
                           ('ownerId', 'other'), ('sharedToId', 'other'),
                           ('permission', 'owner')]:
            grant = {'entityType': 'DOC_SHARE', 'meetingId': 'doc',
                     'ownerId': 'owner', 'sharedToId': 'reader', 'permission': 'read'}
            grant[field] = bad
            self.table.rows[('USER#reader', 'SHAREDDOC#doc')] = grant
            self.assertIsNone(self.read('reader'), field)

    def test_account_requires_exact_current_membership_not_author_or_parent(self):
        account_doc = dict(self.personal, PK='ACCOUNT#child', accountId='child',
                           entityType='ACCOUNT_DOC')
        self.table.rows[('ACCOUNT#child', 'DOC#doc')] = account_doc
        self.assertIsNone(self.read('owner', 'ACCOUNT#child'))
        self.table.rows[('ACCOUNT#parent', 'MEMBER#reader')] = {'accountId': 'parent', 'userId': 'reader'}
        self.assertIsNone(self.read('reader', 'ACCOUNT#child'))
        self.table.rows[('ACCOUNT#child', 'MEMBER#reader')] = {'accountId': 'child', 'userId': 'reader'}
        self.assertEqual(self.read('reader', 'ACCOUNT#child')['content'], '현재 메모')
        del self.table.rows[('ACCOUNT#child', 'MEMBER#reader')]
        self.assertIsNone(self.read('reader', 'ACCOUNT#child'))

    def test_forged_canonical_identity_and_invalid_ids_are_rejected(self):
        self.personal['docId'] = 'other'
        self.assertIsNone(self.read())
        for pk, doc_id in [('USER#../owner', 'doc'), ('OTHER#owner', 'doc'),
                           ('USER#owner', '../doc'), ('USER#owner', 'doc#suffix')]:
            with self.assertRaises(ValueError):
                read_current_document(self.table, 'owner', pk, doc_id)

    def test_storage_errors_are_not_empty_success(self):
        class BrokenTable:
            def get_item(self, **kwargs):
                raise RuntimeError('synthetic storage failure')
        with self.assertRaises(RuntimeError):
            read_current_document(BrokenTable(), 'owner', 'USER#owner', 'doc')


if __name__ == '__main__':
    unittest.main()
