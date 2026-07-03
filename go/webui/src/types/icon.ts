export interface IconMetadata {
  name: string;
  displayName: string;
  category: "ai-provider" | "cloud" | "tool" | "other";
  keywords: string[];
  defaultColor: string;
}
