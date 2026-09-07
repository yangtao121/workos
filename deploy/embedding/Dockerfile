FROM python:3.12-slim-bookworm@sha256:782412e85d0f0984994c290652577d4018aff08145c85b262bb63dc0c7522254
ARG PYPI_MIRROR=https://mirrors.aliyun.com/pypi
COPY deploy/embedding/requirements.txt /tmp/requirements.txt
RUN --mount=type=cache,target=/root/.cache/pip \
    python -m pip install --only-binary=:all: --timeout 30 --retries 2 \
    --index-url "${PYPI_MIRROR}/simple" -r /tmp/requirements.txt \
    && python -m pip check
ENV HF_HUB_OFFLINE=1 HF_HUB_DISABLE_TELEMETRY=1 TOKENIZERS_PARALLELISM=false
USER 1000:1000
WORKDIR /tmp
