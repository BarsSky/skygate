cat >> /workspace/scripts/skygate-dev/deploy-skygate-final.sh <<'PART_FINAL'

echo "STEP7: start skygate"
docker compose up -d skygate 2>&1 > /tmp/skygate-up.log
cat /tmp/skygate-up.log
    echo

    echo "STEP8: wait 25s"
    sleep 25

    echo "Container status:"
    docker ps --format 'table {{.Names}}        {{.Status}}     {{.Ports}}' | grep -E 'skygate|headscale'
    echo

    HTTP_CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://localhost:8080/login 2>&1 || echo 000)
    echo "HTTP localhost:8080/login: $HTTP_CODE"
    echo

    echo "Skygate logs:"
    docker compose logs skygate --tail 30 2>&1 | head -25
    echo

    cat <<"NEXT"

    ✅ Deployment finished!

    Open: http://localhost:8080/login
    Login: skyadmin
    Password: $ADMIN_PASS

    Bootstrap password saved: /tmp/skygate-bootstrap-pass.txt

    NPM proxy setup:
      Domain:     skygate.skynas.ru
      Forward:    192.168.13.69:8080
      SSL:        Request new LE cert
      Force SSL:  ON

    NEXT
    PART_FINAL

    bash -n /workspace/scripts/skygate-dev/deploy-skygate-final.sh && echo "syntax OK"


