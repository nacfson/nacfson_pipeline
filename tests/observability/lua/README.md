# Lua Observability Tests

This directory contains standalone Lua unit tests run with LuaJIT:
- `run_log_contract_tests.lua`: validates `log_contract.lua` filtering, field extraction, timestamp parsing, redaction, and truncation logic without requiring a full Fluent Bit instance.
