import json, sys
sys.path.insert(0, sys.argv[1])
import segno
import segno.encoder as enc

# segno pads a byte-aligned stream with a whole zero byte (8 - 0 % 8 == 8).
# ISO/IEC 18004 7.4.10 pads only a stream that is NOT on a codeword boundary,
# which in byte mode (12 + 8n bits, then a 4-bit terminator) it always is.
# Both decode -- a reader stops at the terminator -- but only the standard's
# is comparable, so that one function is put right and nothing else touched.
def _padding_bits(buff, version, length):
    if length % 8:
        buff.extend([0] * (8 - length % 8))
enc.write_padding_bits = _padding_bits

texts = [
    "docfind",                                                   # version 1
    "otpauth://totp/docfind:ali?secret=JBSWY3DPEHPK3PXP&issuer=docfind",  # ~67 bytes
    "otpauth://totp/Kavir%20docfind:a.example?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=Kavir%20docfind",  # version 6
    "y" * 110,                                                   # version 7: version bits
    "z" * 150,                                                   # version 8: two block sizes
    "w" * 175,                                                   # version 9
    "x" * 200,                                                   # version 10: 16-bit count
]
out = []
for t in texts:
    for mask in (None, 0, 3, 5, 7):
        q = segno.make(t, error='m', mode='byte', mask=mask, boost_error=False, micro=False)
        rows = [''.join('1' if v else '0' for v in row) for row in q.matrix]
        out.append({"text": t, "mask": -1 if mask is None else mask,
                    "chosen": q.mask, "version": q.version, "rows": rows})
json.dump(out, open(sys.argv[2], "w"), separators=(",", ":"))
print(len(out), "grids;", [(o["version"], o["mask"], o["chosen"]) for o in out])
