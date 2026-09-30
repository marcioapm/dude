"""The web app's Vite dev server for the browser tests beside this file."""
import os
import signal
import subprocess
from pathlib import Path
from time import sleep
from urllib.request import urlopen

WEB = Path(__file__).resolve().parents[1]


def start_vite(port):
    """Vite on `port` in its own process group, returned once it answers; stopped again if it never does."""
    # `bun run` spawns vite as a child: only the group reaches both.
    server = subprocess.Popen(
        ["bun", "run", "dev", "--host", "127.0.0.1", "--port", str(port), "--strictPort"],
        cwd=WEB, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
    try:
        for _ in range(100):
            if server.poll() is not None:
                raise RuntimeError("Local Vite server failed to start")
            try:
                with urlopen(f"http://127.0.0.1:{port}", timeout=1):
                    return server
            except OSError:
                sleep(0.1)
        raise RuntimeError("Local Vite server did not become ready")
    except BaseException:
        stop_vite(server)
        raise


def stop_vite(server):
    """SIGTERM to the server's group, 10 s to exit, then SIGKILL and wait."""
    try:
        os.killpg(server.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        server.wait(timeout=10)
    except subprocess.TimeoutExpired:
        pass
    try:
        os.killpg(server.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    server.wait(timeout=10)
