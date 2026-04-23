FROM python:3.11-slim

WORKDIR /app

COPY requirements-container.txt .
RUN pip install --no-cache-dir -r requirements-container.txt

COPY agent.py tools.py ./

RUN python -m compileall -q /app 2>/dev/null || true

EXPOSE 8080

CMD ["python", "-u", "agent.py"]
