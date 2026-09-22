"""Cookie scope shared by browser export and the HTTP session importer."""

ALLOWED_COOKIE_DOMAINS = frozenset({
    'google.com', 'gemini.google.com', 'accounts.google.com',
    'lh3.google.com', 'lh3.googleusercontent.com', 'work.fife.usercontent.google.com',
})
