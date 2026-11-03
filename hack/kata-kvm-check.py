#!/usr/bin/env python3
"""Read-only KVM capability check: create and close an empty VM (no guest)."""
import fcntl
import json
import os

device = os.open("/dev/kvm", os.O_RDWR | os.O_CLOEXEC)
try:
    version = fcntl.ioctl(device, 0xAE00, 0)  # KVM_GET_API_VERSION
    if version != 12:
        raise RuntimeError(f"unsupported KVM API version: {version}")
    vm = fcntl.ioctl(device, 0xAE01, 0)  # KVM_CREATE_VM
    os.close(vm)
    print(json.dumps({"apiVersion": version, "createVM": True}))
finally:
    os.close(device)
