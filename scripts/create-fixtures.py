"""Create disposable text and image-only PDFs for real OCR smoke tests.

Requires Python 3 and Poppler's pdftoppm. No private documents are used.
"""
import pathlib
import subprocess
import sys
import tempfile
import zlib


def write_pdf(path, objects):
    output = bytearray(b"%PDF-1.4\n")
    offsets = [0]
    for number, obj in enumerate(objects, 1):
        offsets.append(len(output))
        output += f"{number} 0 obj\n".encode() + obj + b"\nendobj\n"
    start = len(output)
    output += f"xref\n0 {len(offsets)}\n0000000000 65535 f \n".encode()
    for offset in offsets[1:]:
        output += f"{offset:010} 00000 n \n".encode()
    output += f"trailer\n<< /Size {len(offsets)} /Root 1 0 R >>\nstartxref\n{start}\n%%EOF\n".encode()
    path.write_bytes(output)


def stream(data, attributes=""):
    return f"<< {attributes} /Length {len(data)} >>\nstream\n".encode() + data + b"\nendstream"


def text_pdf(path, text):
    content = f"BT /F1 24 Tf 50 720 Td ({text}) Tj ET".encode()
    write_pdf(path, [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
        stream(content),
    ])


def image_pdf(path, source):
    with tempfile.TemporaryDirectory() as temp:
        base = pathlib.Path(temp) / "page"
        subprocess.run(["pdftoppm", "-singlefile", "-gray", "-scale-to", "1600", str(source), str(base)], check=True, timeout=30, stderr=subprocess.DEVNULL)
        with base.with_suffix(".pgm").open("rb") as image:
            if image.readline().strip() != b"P5":
                raise ValueError("expected a binary grayscale image")
            line = image.readline()
            while line.startswith(b"#"):
                line = image.readline()
            width, height = map(int, line.split())
            if image.readline().strip() != b"255":
                raise ValueError("expected 8-bit image")
            pixels = image.read()
        write_pdf(path, [
            b"<< /Type /Catalog /Pages 2 0 R >>",
            b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
            b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /XObject << /Im1 4 0 R >> >> /Contents 5 0 R >>",
            stream(zlib.compress(pixels), f"/Type /XObject /Subtype /Image /Width {width} /Height {height} /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode"),
            stream(b"q 595 0 0 842 0 0 cm /Im1 Do Q"),
        ])


destination = pathlib.Path(sys.argv[1])
destination.mkdir(parents=True, exist_ok=True)
text_pdf(destination / "text.pdf", "Haftpflicht Versicherung Nachweis 2026")
with tempfile.TemporaryDirectory() as temp:
    source = pathlib.Path(temp) / "source.pdf"
    text_pdf(source, "Hausrat Versicherung Nachweis 2025")
    image_pdf(destination / "scan.pdf", source)
(destination / "broken.pdf").write_bytes(b"This is not a valid PDF")
print(f"Created text.pdf, scan.pdf and broken.pdf in {destination}")
