# Sandbox image for `just login <name>`: runs Claude Code's interactive OAuth
# login with the account directory mounted at /data, so the resulting
# .credentials.json lands on the host and nothing else persists.
FROM node:22-slim
RUN npm install -g @anthropic-ai/claude-code
ENV CLAUDE_CONFIG_DIR=/data
WORKDIR /data
ENTRYPOINT ["claude"]
CMD ["/login"]
