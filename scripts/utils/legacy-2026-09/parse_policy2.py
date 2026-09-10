import json
with open("policy2.json","r",encoding="utf-8") as f:
    d=json.load(f)
print("acls count:", len(d.get("acls",[])))
print()
print("=== first 3 ACLs ===")
for a in d.get("acls",[])[:3]:
    print(json.dumps(a,indent=2))
print()
print("=== tagOwners ===")
for k,v in d.get("tagOwners",{}).items():
    print(" ", k, "->", v)
print()
print("=== ssh rules ===")
print("count:", len(d.get("ssh",[])))
for r in d.get("ssh",[]):
    print(" ", r)
print()
print("=== search for svyat/skyworker in policy ===")
txt=json.dumps(d)
for keyword in ["svyat","skyworker","tagged-devices","dev-skyadmin"]:
    if keyword in txt:
        idx=txt.find(keyword)
        print(f"  '{keyword}' found at idx {idx}:", txt[max(0,idx-30):idx+50])
    else:
        print(f"  '{keyword}' NOT FOUND")
