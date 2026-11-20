"""Connection scenarios for BITS_FAKE_BACKEND=1. Start with `BITS_FAKE_BACKEND=1 go run .`.

Run one from the Starlark console, for example:
  load("connection.star", "drop_mid_answer"); drop_mid_answer()

A turn runs apart from the request that sent it, so breaking the network
does not stop it: the CLI reconnects and catches up.
"""

def words(n, prefix = "word"):
    return " ".join(["%s-%d" % (prefix, i) for i in range(n)])

def drop_mid_answer():
    """The connection breaks halfway through the answer."""
    say("Here is the first half: " + words(20) + ". ")
    drop()
    sleep("1s")
    say("And the second half: " + words(20, "more") + ".")

def drop_at_tool_pause():
    """The connection breaks as the turn pauses on a client tool."""
    say("Listing the files.")
    drop()
    r = call("list_files", {"path": "."})
    say("Listed: %s" % ("go.mod" in r.output))

def offline_send(duration = "5s"):
    """The network goes down for a while; a prompt sent meanwhile fails before it is delivered."""
    say("Going offline for %s. Send something now." % duration)
    offline(duration)
    sleep(duration)
    say("Back online.")

def restart_mid_turn():
    """The service loses the turn halfway through, without saying it ended."""
    say("Starting a long answer: " + words(10) + " ")
    restart()

def turn_elsewhere(after = "2s"):
    """Another session starts a turn here while this one is idle."""
    send_from_elsewhere('say("Answering a prompt from another session: %s."); sleep("3s"); say("Done.")' % words(15), after = after)
    say("Another session will send a prompt in %s." % after)

def queued_during_tool_pause():
    """Another session queues a prompt while this turn pauses on a client tool."""
    send_from_elsewhere('say("The other session\'s turn."); call("list_files", {"path": "."})')
    say("Pausing on a client tool.")
    r = call("list_files", {"path": "."})
    say("This session's tool ran: %s" % r.ok)

def queued_turns_with_tools(delay = "1s", other = "5s", drop_at_pause = True):
    """Two prompts from another session queue behind this turn; the second calls a client tool, and the connection drops as this turn pauses on its own."""
    send_from_elsewhere('say("Other session, first prompt."); sleep("%s")' % other, after = "1ms")
    send_from_elsewhere('r = call("list_files", {"path": "."}); say("Other session ran its tool here: %s" % r.ok)', after = "2ms")
    sleep(delay)
    if drop_at_pause:
        drop()
    r = call("list_files", {"path": "."})
    say("Listed: %s" % ("go.mod" in r.output))

def remote_tool_pause():
    """Another session's turn pauses on a client tool nobody here answers; a prompt sent meanwhile waits for that answer."""
    send_from_elsewhere('say("The other session\'s turn."); call("list_files", {"path": "."})')
    say("Another session is waiting on a client tool; a prompt sent now waits for its answer.")

def remote_tool_pause_answered(after = "3s"):
    """Another session's turn pauses on a client tool, and that session answers it after a while."""
    send_from_elsewhere('say("The other session\'s turn."); r = call("list_files", {"path": "."}); say("The other session answered: %s" % r.ok)')
    answer_from_elsewhere(after = after)
    say("Another session is waiting on a client tool and answers it in %s." % after)

def lost_started_marker():
    """Another session's turn starts without this session seeing its start; its prompt and answer still show after this turn."""
    lose_turn_status("started")
    send_from_elsewhere('say("The other session\'s turn.")', after = "1ms")
    sleep("50ms")
    say("This session's turn.")

def simultaneous_send():
    """Another session sends at the same instant as this session's next prompt, the accepted limit of holding prompts back."""
    send_from_elsewhere('say("The other session\'s turn."); call("list_files", {"path": "."})', simultaneous = True)
    say("Send any prompt: the other session's prompt reaches the conversation just ahead of it.")
