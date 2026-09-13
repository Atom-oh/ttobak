"""Bounded checkpoints of acknowledged tool pairs for interrupted async QA."""
import copy
import logging

from async_jobs import JobDeadline, deadline, encode

logger = logging.getLogger(__name__)
CLEANUP_SECONDS = 2
MAX_CHECKPOINT_BYTES = 128 * 1024
INTERRUPTED = (
    "응답이 중단되었습니다. 확인된 이전 도구 결과를 확인하세요. "
    "다른 작업의 완료 여부는 확인되지 않았을 수 있으므로 자동으로 다시 실행하지 마세요."
)


class DeadlineHistory:
    def __init__(self):
        self.checkpoint = None

    def capture(self, messages, tool_results, state, details):
        """Keep only paired results already returned and tracked, never pending calls."""
        if not messages or not tool_results or state.get("replayable") is not True:
            return False
        try:
            ids = {result["toolResult"]["toolUseId"] for result in tool_results}
            last = messages[-1]
            if last.get("role") != "assistant":
                return False
            tools = [block for block in last["content"]
                     if block.get("toolUse", {}).get("toolUseId") in ids]
            if len(tools) != len(ids) or len(ids) != len(tool_results):
                return False
            saved_state = copy.deepcopy(state)
            delivery = saved_state.get("_delivery")
            proof = delivery.finish(saved_state) if delivery is not None else None
            candidate = messages[:-1] + [
                {"role": "assistant", "content": tools},
                {"role": "user", "content": tool_results},
                {"role": "assistant", "content": [{"text": INTERRUPTED}]},
            ]
            # Bound the persisted history, metadata and validation proof together.
            # encode preserves the existing integral-Decimal JSON convention.
            encode({"messages": candidate, "dependencies": saved_state.get("dependencies", []),
                    "details": details, "delivery": proof}, MAX_CHECKPOINT_BYTES)
            self.checkpoint = (copy.deepcopy(candidate), saved_state, copy.deepcopy(details))
            return True
        except Exception:
            # Do not replace a prior valid checkpoint with an untracked/oversized one.
            return False

    def preserve(self, validate, save):
        if self.checkpoint is None:
            return False
        messages, state, details = self.checkpoint
        try:
            with deadline(CLEANUP_SECONDS):
                validate(state)  # Current authorization and read-only source proof.
                return save(messages, state, details) is True
        except (JobDeadline, Exception) as error:
            logger.warning("Deadline history cleanup was not acknowledged (%s)", type(error).__name__)
            return False
