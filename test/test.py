#!/usr/bin/env python3
import sys
import subprocess
import platform

# Print Hello World
print("Hello World")

# Read and print command-line arguments
print(f"\nScript path: {sys.argv[0]}")
print(f"Argument count: {len(sys.argv) - 1}")
if len(sys.argv) > 1:
    print("Arguments:")
    for i, arg in enumerate(sys.argv[1:], 1):
        print(f"  Argument {i}: {arg}")
else:
    print("No arguments supplied")
print()
# Read each line from stdin (supports pipes and noninteractive mode)
try:
    while True:
        line = sys.stdin.readline()
        # Stop on EOF or an empty line
        if not line or line.strip() == '':
            break
        # Strip the trailing newline and print the line
        print(f"Line: {line.rstrip()}")
except (EOFError, KeyboardInterrupt):
    # Ignore missing input or user interruption
    pass

# Ping continuously
try:
    # Choose ping arguments for the operating system
    if platform.system().lower() == 'windows':
        # Windows: -t means continuous ping
        subprocess.run(['ping', '-t', '1.1.1.1'])
    else:
        # Linux/macOS: continuous ping is the default; no -t needed
        subprocess.run(['ping', '1.1.1.1'])
except KeyboardInterrupt:
    print("\nPing stopped")

