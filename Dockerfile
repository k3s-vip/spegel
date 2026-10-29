FROM alpine:latest
ARG TARGETARCH
COPY .git/$TARGETARCH /spegel
ENTRYPOINT ["/spegel"]
