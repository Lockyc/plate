// The quality harness's Quartz rasteriser: `quartz <in.pdf> <page> <dpi>
// <out.png>` draws one page of a PDF through CoreGraphics, as macOS Preview
// does, into an off-screen bitmap on white, and writes it as an sRGB PNG.
// It opens no window.

import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

func die(_ message: String) -> Never {
  FileHandle.standardError.write("quartz: \(message)\n".data(using: .utf8)!)
  exit(1)
}

let args = CommandLine.arguments
guard args.count == 5, let n = Int(args[2]), let dpi = Double(args[3]), dpi > 0 else {
  die("usage: quartz <in.pdf> <page> <dpi> <out.png>")
}
guard let doc = CGPDFDocument(URL(fileURLWithPath: args[1]) as CFURL) else { die("cannot read \(args[1])") }
guard n >= 1, n <= doc.numberOfPages, let page = doc.page(at: n) else { die("no page \(n)") }
let box = page.getBoxRect(.mediaBox)
let scale = dpi / 72
let w = Int((box.width * scale).rounded()), h = Int((box.height * scale).rounded())
guard let space = CGColorSpace(name: CGColorSpace.sRGB),
  let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0, space: space,
    bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)
else { die("cannot make a \(w)x\(h) bitmap") }
ctx.setFillColor(CGColor(srgbRed: 1, green: 1, blue: 1, alpha: 1))
ctx.fill(CGRect(x: 0, y: 0, width: w, height: h))
ctx.scaleBy(x: scale, y: scale)
ctx.translateBy(x: -box.minX, y: -box.minY)
ctx.drawPDFPage(page)
guard let image = ctx.makeImage(),
  let dst = CGImageDestinationCreateWithURL(URL(fileURLWithPath: args[4]) as CFURL, UTType.png.identifier as CFString, 1, nil)
else { die("cannot write \(args[4])") }
CGImageDestinationAddImage(dst, image, nil)
guard CGImageDestinationFinalize(dst) else { die("cannot write \(args[4])") }
