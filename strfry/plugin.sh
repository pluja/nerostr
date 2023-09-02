#!/bin/bash
API_HOST=${API_URL:-'nerostr:8080'}

while IFS= read -r line; do
    type=$(echo $line | jq -r '.type')
    id=$(echo $line | jq -r '.event.id')
    pubkey=$(echo $line | jq -r '.event.pubkey')

    if [ "$type" = "lookback" ]; then
        continue
    fi

    if [ "$type" != "new" ]; then
        echo "unexpected request type" >&2
        continue
    fi

    apiResponse=$(curl -s "${API_HOST}/api/status/${pubkey}")

    action=$(echo $apiResponse | jq -r '.action')

    res='{"id": "'$id'", "action": "'$action'"}'
    
    if [ "$action" = "reject" ]; then
        res=$(echo $res | jq '. + { "msg": "blocked: you must pay admission fee. visit relay url on browser." }')
    fi

    echo $res
done