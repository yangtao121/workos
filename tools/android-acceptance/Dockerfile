FROM eclipse-temurin:21-jdk-jammy AS java
FROM workos-network-test:local
USER root
COPY --from=java /opt/java/openjdk /opt/java/openjdk
ENV JAVA_HOME=/opt/java/openjdk
ENV ANDROID_SDK_ROOT=/sdk ANDROID_HOME=/sdk
ENV PATH=/opt/java/openjdk/bin:/sdk/platform-tools:/sdk/emulator:/sdk/cmdline-tools/latest/bin:$PATH
LABEL org.opencontainers.image.title="WorkOS headless Android build and emulator acceptance"
