FROM --platform=linux/arm64 ghcr.io/astral-sh/uv:python3.11-bookworm-slim

WORKDIR /app

COPY requirements-container.txt .
RUN uv pip install --system --no-cache -r requirements-container.txt

COPY agent.py tools.py ./

EXPOSE 8080

CMD ["uvicorn", "agent:app", "--host", "0.0.0.0", "--port", "8080"]
