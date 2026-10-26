# syntax=docker/dockerfile:1
# hotbedvmd — QEMU-in-Docker VM runtime for hotbed.
#
# One container = one VM. The container needs only:
#   --device /dev/kvm          (optional; falls back to very-slow TCG without)
#   -v hotbedvmd-store:/store    (master.qcow2 + per-VM disks/seeds/serial)
#   -v <host seed dir>:/seed-src:ro   (user-data + meta-data, first boot)
#   -v <host share dir>:/share        (when --share was given)
#
# Lifecycle is plain Docker: `docker run -d` boots the VM, `docker stop`
# sends SIGTERM -> entry sends ACPI powerdown to the monitor socket,
# `docker start` reboots it, `docker rm` throws it away. No libvirt.
FROM debian:trixie-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash \
      socat \
      qemu-system-x86 \
      qemu-utils \
      xorriso \
      ca-certificates \
 && rm -rf /var/lib/apt/lists/*

COPY vm/hotbedvmd/entry /usr/local/bin/hotbedvmd-entry
RUN chmod 0755 /usr/local/bin/hotbedvmd-entry

ENTRYPOINT ["/usr/local/bin/hotbedvmd-entry"]
