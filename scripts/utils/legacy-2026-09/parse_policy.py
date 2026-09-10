import json, sys
with open("/tmp/policy.json","rb") as f:
    raw=f.read()
print("first 100 bytes raw:", raw[:100])
# Try multiple encodings
for enc in ["utf-16","utf-8","utf-16-le","utf-16-be"]:
    try:
        txt=raw.decode(enc)
        # try json
        try:
            d=json.loads(txt)
            print(f"OK with {enc}")
            print("acls:", len(d.get("acls",[])))
            print("tagOwners:", json.dumps(d.get("tagOwners",{}),indent=2)[:300])
            print("ssh:", len(d.get("ssh",[])))
            for r in d.get("ssh",[])[:3]:
                print("  ssh:", r)
            sys.exit(0)
        except json.JSONDecodeError as e:
            print(f"json fail with {enc}: {e}")
    except UnicodeDecodeError as e:
        print(f"decode fail with {enc}: {e}")
