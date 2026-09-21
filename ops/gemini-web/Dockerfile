FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY --chmod=0444 worker.py session_contract.py .
USER 1000:1000
EXPOSE 8091
CMD ["python", "-u", "worker.py"]
