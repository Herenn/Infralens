# InfraLens Frontend Dockerfile
# =============================
# Multi-stage build with Nginx for production

# Build stage
FROM node:20-alpine AS builder

WORKDIR /app

# Copy package files
COPY package*.json ./
RUN npm ci

# Copy source
COPY . .

# Build for production
RUN npm run build

# Runtime stage - Nginx
FROM nginx:alpine

# Copy built assets
COPY --from=builder /app/dist /usr/share/nginx/html

# nginx config, as a template: the image's entrypoint renders it into
# /etc/nginx/conf.d/default.conf with INFRALENS_BACKEND_URL substituted (see
# the top of nginx.conf). The default is the docker-compose service name.
COPY nginx.conf /etc/nginx/templates/default.conf.template
RUN rm -f /etc/nginx/conf.d/default.conf
ENV INFRALENS_BACKEND_URL=http://backend:8080

EXPOSE 3000

CMD ["nginx", "-g", "daemon off;"]
