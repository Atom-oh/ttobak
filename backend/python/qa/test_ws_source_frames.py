import json
import unittest
from unittest import mock

import test_handler
from ws_source_frames import completion_frames, wire_bytes, MAX_SOURCE_BYTES

handler = test_handler.handler


class TestSourceFrames(unittest.TestCase):
    def payload(self):
        details = [
            {'resourceKind': 'personalDocument', 'resourceId': 'doc-' + str(i),
             'sourcePK': 'USER#owner', 'sourceSK': 'DOC#doc-' + str(i),
             'sourceRevision': 'a' * 64, 'uri': 'ttobak://source/' + str(i),
             'title': '한' * 200, 'provenanceScope': 'validated_history'}
            for i in range(40)
        ]
        return {'type': 'answer_complete', 'sessionId': 'chat-synthetic', 'answer': 'Known synthetic answer',
                'sources': [item['uri'] for item in details], 'sourceDetails': details, 'toolsUsed': []}

    def test_many_history_sources_are_delivered_completely_below_each_frame_limit(self):
        payload = self.payload()
        self.assertGreater(len(wire_bytes(payload)), handler.WS_FRAME_BUDGET_BYTES)
        frames = completion_frames(payload)
        self.assertGreater(len(frames), 1)
        self.assertTrue(all(len(wire_bytes(frame)) <= handler.WS_FRAME_BUDGET_BYTES for frame in frames))
        self.assertEqual([value for frame in frames[:-1] for value in frame['sources']], payload['sources'])
        self.assertEqual([value for frame in frames[:-1] for value in frame['sourceDetails']], payload['sourceDetails'])
        self.assertEqual([frame['sourceBatchIndex'] for frame in frames[:-1]], list(range(len(frames) - 1)))
        self.assertEqual(frames[-1]['sourceBatchCount'], len(frames) - 1)
        self.assertEqual(frames[-1]['answer'], payload['answer'])
        gateway = mock.Mock()
        self.assertTrue(handler._post_ws_completion(gateway, 'connection', payload, 1))
        sent = [json.loads(call.kwargs['Data']) for call in gateway.post_to_connection.call_args_list]
        self.assertEqual(sent, frames)

    def test_small_completion_keeps_the_existing_wire_shape(self):
        payload = dict(self.payload(), sources=[], sourceDetails=[])
        self.assertEqual(completion_frames(payload), [payload])

    def test_oversized_answer_or_single_source_fails_before_any_network_write(self):
        for payload in (
            dict(self.payload(), answer='x' * 30_001),
            dict(self.payload(), sources=[], sourceDetails=[{'uri': 'x' * 30_001}]),
            dict(self.payload(), sources=['x' * (MAX_SOURCE_BYTES + 1)]),
        ):
            gateway = mock.Mock()
            with self.assertRaises(handler.WebSocketDeliveryError):
                handler._post_ws_completion(gateway, 'connection', payload, 1)
            gateway.post_to_connection.assert_not_called()

    def test_source_frame_delivery_failure_never_sends_a_successful_completion(self):
        gateway = mock.Mock()
        gateway.exceptions.GoneException = type('Gone', (Exception,), {})
        gateway.exceptions.PayloadTooLargeException = type('TooLarge', (Exception,), {})
        for error in (RuntimeError('unavailable'), gateway.exceptions.GoneException()):
            gateway.post_to_connection.reset_mock(side_effect=True)
            gateway.post_to_connection.side_effect = error
            if isinstance(error, gateway.exceptions.GoneException):
                self.assertFalse(handler._post_ws_completion(gateway, 'connection', self.payload(), 1))
            else:
                with self.assertRaises(handler.WebSocketDeliveryError):
                    handler._post_ws_completion(gateway, 'connection', self.payload(), 1)
            sent = [json.loads(call.kwargs['Data']) for call in gateway.post_to_connection.call_args_list]
            self.assertFalse(any(frame['type'] == 'answer_complete' for frame in sent))

    def test_old_or_unknown_clients_keep_explicit_size_failure_without_partial_sources(self):
        for version in (0, 2, True, '1', None):
            gateway = mock.Mock()
            with self.assertRaises(handler.WebSocketDeliveryError):
                handler._post_ws_completion(gateway, 'connection', self.payload(), version)
            gateway.post_to_connection.assert_not_called()


if __name__ == '__main__':
    unittest.main()
