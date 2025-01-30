proxies-snapshot:
    curl -v -H "Authorization: Bearer $PROXYHUB_TOKEN" http://localhost:8080/api/proxies

proxies-live:
    curl -v -H "Authorization: Bearer $PROXYHUB_TOKEN" http://localhost:8080/api/proxies/live
