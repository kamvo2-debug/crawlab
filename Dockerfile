ARG CRAWLAB_TAG=latest

FROM crawlabteam/crawlab-backend:${CRAWLAB_TAG} AS backend-build

FROM crawlabteam/crawlab-frontend:${CRAWLAB_TAG} AS frontend-build

FROM crawlabteam/crawlab-base:${CRAWLAB_TAG}

# Copy files
COPY --from=backend-build /go/bin/crawlab /usr/local/bin/crawlab-server
COPY --from=frontend-build /app/dist /app/dist
COPY ./backend/conf /app/backend/conf
COPY ./docker/nginx/crawlab.conf /etc/nginx/conf.d
COPY ./docker/bin/docker-init.sh /app/bin/docker-init.sh
COPY ./docker/bin/health-check.sh /app/bin/health-check.sh

# Start backend
CMD ["/bin/bash", "/app/bin/docker-init.sh"]

# Frontend port
EXPOSE 8080

# Healthcheck for backend
HEALTHCHECK --interval=1m --timeout=3s \
  CMD bash /app/bin/health-check.sh || exit 1