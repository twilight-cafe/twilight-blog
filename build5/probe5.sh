#!/bin/bash
# Round-5 dynamic attack probes (isolated instance)
export PATH="/c/Windows/System32:/c/Windows:/c/Program Files/Git/cmd:/c/Users/twilight/.workbuddy/binaries/PortableGit/versions/1.2.0/bin:/c/Users/twilight/.workbuddy/binaries/PortableGit/versions/1.2.0/usr/bin:$PATH"
export NO_PROXY="127.0.0.1,localhost"
export no_proxy="127.0.0.1,localhost"
BASE="http://127.0.0.1:8107"
OUT="D:/Files/development/twilight-blog/build5/probe5-results.txt"
C="curl -s -o /dev/null -w %{http_code}"
CB="curl -s -o NUL -w %{http_code}"
: > "$OUT"
say(){ echo "$*" | tee -a "$OUT"; }

say "=== R5 dynamic probes ==="

# --- 0. health + install ---
say "GET / -> $($C $BASE/)"
say "install_status -> $($C $BASE/install_status)"
say "POST /install (weak pwd) -> $(curl -s -o /dev/null -w %{http_code} -X POST -H 'Content-Type: application/json' -d '{"password":"123"}' $BASE/install)"
say "POST /install (strong) -> $(curl -s -o /dev/null -w %{http_code} -X POST -H 'Content-Type: application/json' -d '{"password":"Pentest#2026Round5!"}' $BASE/install)"
say "POST /install replay -> $(curl -s -o /dev/null -w %{http_code} -X POST -H 'Content-Type: application/json' -d '{"password":"Pentest#2026Round5!"}' $BASE/install)"

# --- 1. login (admin) ---
rm -f D:/Files/development/twilight-blog/build5/instance/cookies.txt
curl -s -c D:/Files/development/twilight-blog/build5/instance/cookies.txt -o /dev/null -X POST -H 'Content-Type: application/json' -d '{"username":"admin","password":"Pentest#2026Round5!"}' $BASE/api/auth/login
TOKEN=$(/usr/bin/grep tw_csrf D:/Files/development/twilight-blog/build5/instance/cookies.txt | awk '{print $NF}')
say "login done, csrf_len=${#TOKEN}"

# --- 2. path normalization / traversal on admin API (unauthenticated) ---
say "--- traversal (unauth, expect 401/403/404 never 200 data) ---"
for p in "/api/../api/admin/users" "//api/admin/users" "/./api/admin/users" "/api/%2e%2e/api/admin/users" "/api/admin/../admin/users" "/API/admin/users" "/api/admin/users/" "/api/ADMIN/users" "/api/admin/users%20" "/api%2Fadmin%2Fusers"; do
  code=$(curl -s -o /dev/null -w %{http_code} --path-as-is "$BASE$p")
  say "  GET $p -> $code"
done
# with admin session (sameHost/origin irrelevant for GET)
for p in "/api/../api/admin/users" "//api/admin/users" "/api/admin/../admin/users"; do
  code=$(curl -s -o /dev/null -w %{http_code} --path-as-is -b D:/Files/development/twilight-blog/build5/instance/cookies.txt "$BASE$p")
  say "  GET(auth) $p -> $code"
done

# --- 3. method abuse ---
say "--- method abuse ---"
say "  TRACE /api/auth/login -> $(curl -s -o /dev/null -w %{http_code} -X TRACE $BASE/api/auth/login)"
say "  PATCH /api/articles -> $(curl -s -o /dev/null -w %{http_code} -X PATCH -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H 'X-CSRF-Token: '"$TOKEN" $BASE/api/articles)"
say "  GET /api/admin/users (auth) -> $(curl -s -o /dev/null -w %{http_code} -b D:/Files/development/twilight-blog/build5/instance/cookies.txt $BASE/api/admin/users)"
say "  PUT /api/articles (auth, no body) -> $(curl -s -o /dev/null -w %{http_code} -X PUT -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H 'X-CSRF-Token: '"$TOKEN" -H 'Content-Type: application/json' -d '{}' $BASE/api/articles)"
say "  DELETE /api/articles -> $(curl -s -o /dev/null -w %{http_code} -X DELETE -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H 'X-CSRF-Token: '"$TOKEN" $BASE/api/articles)"
say "  OPTIONS /api/auth/login -> $(curl -s -o /dev/null -w %{http_code} -X OPTIONS $BASE/api/auth/login)"

# --- 4. CSRF enforcement ---
say "--- CSRF ---"
say "  PATCH /api/me w/o CSRF (auth) -> $(curl -s -o /dev/null -w %{http_code} -X PATCH -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H 'Content-Type: application/json' -d '{"nickname":"x"}' $BASE/api/me)"
say "  PATCH /api/me wrong CSRF -> $(curl -s -o /dev/null -w %{http_code} -X PATCH -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H 'X-CSRF-Token: deadbeef' -H 'Content-Type: application/json' -d '{"nickname":"x"}' $BASE/api/me)"
say "  PATCH /api/me correct CSRF, opaque Origin null -> $(curl -s -o /dev/null -w %{http_code} -X PATCH -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H 'X-CSRF-Token: '"$TOKEN"' -H 'Origin: null' -H 'Content-Type: application/json' -d '{"nickname":"x"}' $BASE/api/me)"
say "  PATCH /api/me correct CSRF, evil Origin -> $(curl -s -o /dev/null -w %{http_code} -X PATCH -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H 'X-CSRF-Token: '"$TOKEN"' -H 'Origin: http://evil.example' -H 'Content-Type: application/json' -d '{"nickname":"x"}' $BASE/api/me)"

# --- 5. weird ids ---
say "--- weird ids ---"
for id in "+1" "01" "1e2" "0" "-1" "9223372036854775807" "99999999999999999999" "%201" "1.0"; do
  code=$(curl -s -o /dev/null -w %{http_code} --path-as-is "$BASE/api/articles/$id")
  say "  GET /api/articles/$id -> $code"
done

# --- 6. duplicate query params ---
say "--- duplicate params ---"
say "  page dup -> $(curl -s -o /dev/null -w %{http_code} "$BASE/api/articles?page=1&page_size=12&page=999999999")"
say "  page_size dup -> $(curl -s -o /dev/null -w %{http_code} "$BASE/api/articles?page=1&page_size=12&page_size=9999")"

# --- 7. admin content: XSS comment probe (admin comments go through sanitizer) ---
say "--- stored XSS probe via admin comment ---"
ART=$(curl -s -X POST -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H "X-CSRF-Token: $TOKEN" -H 'Content-Type: application/json' -d '{"title":"R5 probe","content":"body"}' $BASE/api/admin/articles)
AID=$(echo "$ART" | /usr/bin/sed -n 's/.*"id":\([0-9]*\).*/\1/p' | head -1)
say "  created article id=$AID"
CMT=$(curl -s -X POST -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H "X-CSRF-Token: $TOKEN" -H 'Content-Type: application/json' -d '{"target_type":"article","target_id":'"$AID"',"content":"<script>alert(1)</script><img src=x onerror=alert(2)>[x](javascript:alert(3))"}' $BASE/api/comments)
say "  comment create -> $(echo "$CMT" | head -c 120)"
say "  comment read: $(curl -s "$BASE/api/comments?target_type=article&target_id=$AID&page=1&page_size=12" | /usr/bin/grep -o '<script>\|onerror\|javascript:alert' | sort -u | tr '\n' ',')"

# --- 8. upload as admin: scriptable content must be rejected ---
say "--- upload scriptable content ---"
printf '<html><body>hi</body></html>' > D:/Files/development/twilight-blog/build5/instance/evil.html
say "  html upload -> $(curl -s -o /dev/null -w %{http_code} -X POST -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H "X-CSRF-Token: $TOKEN" -F "files=@D:/Files/development/twilight-blog/build5/instance/evil.html" $BASE/api/admin/media)"
printf 'GIF89a<script>alert(1)</script>' > D:/Files/development/twilight-blog/build5/instance/evil.gif
say "  gif-with-script upload -> $(curl -s -o /dev/null -w %{http_code} -X POST -b D:/Files/development/twilight-blog/build5/instance/cookies.txt -H "X-CSRF-Token: $TOKEN" -F "files=@D:/Files/development/twilight-blog/build5/instance/evil.gif" $BASE/api/admin/media)"

# --- 9. security headers ---
say "--- headers ---"
curl -s -D - -o /dev/null "$BASE/" | /usr/bin/grep -i "content-security-policy\|x-content-type\|x-frame\|strict-transport" | while read -r line; do say "  ${line%%$'\r'}"; done

# --- 10. cookie flags ---
say "--- cookie flags (login response) ---"
curl -s -D - -o /dev/null -X POST -H 'Content-Type: application/json' -d '{"username":"admin","password":"Pentest#2026Round5!"}' "$BASE/api/auth/login" | /usr/bin/grep -i "set-cookie" | while read -r line; do say "  ${line%%$'\r'}"; done

# --- 11. search ---
say "--- search ---"
say "  search -> $(curl -s -o /dev/null -w %{http_code} "$BASE/api/search?q=R5&page=1&page_size=12")"
say "  search no q -> $(curl -s -o /dev/null -w %{http_code} "$BASE/api/search?page=1&page_size=12")"
say "  search huge q -> $(curl -s -o /dev/null -w %{http_code} "$BASE/api/search?q=$(printf 'a%.0s' {1..500})&page=1&page_size=12")"

# --- 12. comment target weirdness ---
say "--- comment target ids ---"
for tid in "+1" "01" "1e2" "0" "-1" "99999999999999999999"; do
  code=$(curl -s -o /dev/null -w %{http_code} "$BASE/api/comments?target_type=article&target_id=$tid&page=1&page_size=12")
  say "  target_id=$tid -> $code"
done

say "=== done ==="
