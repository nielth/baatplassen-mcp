FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim

WORKDIR /app
COPY pyproject.toml uv.lock README.md ./
COPY src ./src
RUN uv sync --frozen --no-dev

ENV PYTHONDONTWRITEBYTECODE=1
EXPOSE 8000
CMD [".venv/bin/baatplassen", "serve", "--http"]
