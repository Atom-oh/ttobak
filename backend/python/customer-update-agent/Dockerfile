# Build context: backend/python (the validated fetcher is shared with crawler/).
#   docker build -f customer-update-agent/Dockerfile backend/python
FROM --platform=linux/arm64 ghcr.io/astral-sh/uv:python3.13-bookworm-slim

WORKDIR /app

COPY customer-update-agent/requirements-container.txt .
RUN uv pip install --system --no-cache -r requirements-container.txt

# The fetcher and its only local import (news_pipeline.canonicalize_url) sit
# next to the agent modules, so fetching.py imports it directly. Tests are not copied.
COPY crawler/article_fetcher.py crawler/news_pipeline.py ./
COPY customer-update-agent/app.py customer-update-agent/checks.py \
     customer-update-agent/contract.py customer-update-agent/fetching.py \
     customer-update-agent/gate.py customer-update-agent/ledger.py \
     customer-update-agent/llm.py customer-update-agent/orchestrator.py \
     customer-update-agent/pipeline.py customer-update-agent/reader.py \
     customer-update-agent/researcher.py customer-update-agent/reviewer.py \
     customer-update-agent/sealing.py customer-update-agent/store.py \
     customer-update-agent/tools.py customer-update-agent/writer.py ./

EXPOSE 8080

CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8080"]
