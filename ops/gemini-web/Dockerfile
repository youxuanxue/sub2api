FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY --chmod=0444 worker.py session_contract.py .
USER 1000:1000
EXPOSE 8091
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8091/readyz', timeout=3).close()"
CMD ["python", "-u", "worker.py"]
