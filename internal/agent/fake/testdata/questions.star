def demo():
    r = call("ask_user_question", {"questions": [
        {"question": "Which region should we investigate?", "options": [
            {"label": "US (Recommended)", "description": "Start with the US production environment."},
            {"label": "EU", "description": "Investigate the EU production environment."},
        ]},
        {"question": "Which service should we focus on?", "options": [
            {"label": "API", "description": "Inspect API latency and error rates."},
            {"label": "Worker", "description": "Inspect background jobs and queue delays."},
        ]},
    ]})
    if r.ok:
        say("Demo continued after receiving your answers.\n\n" + r.output)
    else:
        say("Demo continued without answers.\n\n" + r.output)
