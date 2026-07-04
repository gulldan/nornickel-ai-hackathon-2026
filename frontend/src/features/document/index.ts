// Public API of the document feature. Other features import only from here.
export * from "./api";
export * from "./docAdapter";
export * from "./useDocPreview";
export { DocPreviewSheet } from "./ui/DocPreviewSheet";
export { useAttachedUploads, type AttachedUpload } from "./useAttachedUploads";
export { DocumentPreview } from "./ui/DocumentPreview";
export { OriginalDocViewer } from "./ui/OriginalDocViewer";
export type { PdfHighlight } from "./ui/PdfViewer";
