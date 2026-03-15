"""Bedrock Haiku question detection for meeting transcripts."""

import json

import boto3
from botocore.exceptions import ClientError


class QuestionDetector:
    """Detects questions in meeting transcripts using Bedrock Claude Haiku."""

    def __init__(
        self,
        region_name: str = "ap-northeast-2",
        model_id: str = "anthropic.claude-haiku-4-5-20251001-v1:0",
    ):
        """Initialize the Bedrock client.

        Args:
            region_name: AWS region for Bedrock
            model_id: Bedrock model ID for Claude Haiku
        """
        self.client = boto3.client("bedrock-runtime", region_name=region_name)
        self.model_id = model_id

    def detect(self, recent_sentences: list[str]) -> list[str]:
        """Detect questions in recent meeting sentences.

        Args:
            recent_sentences: List of recent sentences (typically last 3)

        Returns:
            List of detected question strings. Empty list if no questions or on error.
        """
        if not recent_sentences:
            return []

        # Join sentences for context
        text = "\n".join(recent_sentences)

        # Prompt for question detection
        prompt = (
            "다음 회의 대화에서 질문을 찾아 JSON 배열로 반환하세요. "
            "질문이 없으면 빈 배열 []을 반환하세요.\n\n"
            f"대화:\n{text}"
        )

        try:
            response = self.client.converse(
                modelId=self.model_id,
                messages=[
                    {
                        "role": "user",
                        "content": [{"text": prompt}],
                    }
                ],
                inferenceConfig={
                    "maxTokens": 512,
                    "temperature": 0.0,
                },
            )

            # Extract response text
            output = response.get("output", {})
            message = output.get("message", {})
            content = message.get("content", [])

            if not content:
                return []

            response_text = content[0].get("text", "").strip()

            # Parse JSON array from response
            # Handle potential markdown code blocks
            if response_text.startswith("```"):
                # Remove markdown code block formatting
                lines = response_text.split("\n")
                response_text = "\n".join(
                    line for line in lines if not line.startswith("```")
                ).strip()

            questions = json.loads(response_text)

            if isinstance(questions, list):
                return [q for q in questions if isinstance(q, str)]

            return []

        except (ClientError, json.JSONDecodeError, KeyError, IndexError) as e:
            # Don't crash the pipeline on errors
            print(f"Question detection error: {e}")
            return []
