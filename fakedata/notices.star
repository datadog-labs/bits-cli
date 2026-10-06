"""Scenarios for previewing local TUI messages with BITS_FAKE_BACKEND=1.

In the TUI, enter one of these prompts:
  load("fakedata/notices.star", "rate_limit"); rate_limit()
  load("fakedata/notices.star", "service_error"); service_error()
  load("fakedata/notices.star", "network_error"); network_error()

Then use /notacommand, /copy, and /new to see local command messages.
"""

def rate_limit():
    say("Partial answer before the assistant was rate limited.")
    fail(429)

def service_error():
    say("Partial answer before the assistant service failed.")
    fail(503)

def network_error():
    say("Partial answer before the connection failed.")
    fail("net")
