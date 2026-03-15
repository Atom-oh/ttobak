"""Amazon Translate client for real-time translation."""

import boto3
from botocore.exceptions import ClientError


class Translator:
    """Wrapper around Amazon Translate for real-time translation."""

    def __init__(self, region_name: str = "ap-northeast-2"):
        """Initialize the Amazon Translate client.

        Args:
            region_name: AWS region for the Translate service
        """
        self.client = boto3.client("translate", region_name=region_name)

    def translate(
        self,
        text: str,
        source_lang: str = "ko",
        target_lang: str = "en",
    ) -> str:
        """Translate text from source language to target language.

        Args:
            text: Text to translate
            source_lang: Source language code (default: Korean)
            target_lang: Target language code (default: English)

        Returns:
            Translated text string
        """
        # Handle empty text gracefully
        if not text or not text.strip():
            return ""

        try:
            response = self.client.translate_text(
                Text=text,
                SourceLanguageCode=source_lang,
                TargetLanguageCode=target_lang,
            )
            return response.get("TranslatedText", "")
        except ClientError as e:
            # Log error but don't crash the pipeline
            print(f"Translation error: {e}")
            return ""
