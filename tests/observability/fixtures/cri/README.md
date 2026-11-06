# CRI Log Fixtures

This directory contains Container Runtime Interface (CRI) format log files used for testing Fluent Bit log collection and VictoriaLogs ingestion:
- Format: `<RFC3339Nano> <stdout|stderr> <P|F> <text>`
- Fixtures test conforming lines, split 16 KiB chunks, crash traces, forged platform fields, bad timestamps, unparseable lines, and redaction.
