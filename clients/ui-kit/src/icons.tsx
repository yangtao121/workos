const paths = {
  home: "m3 10 9-7 9 7v10H3V10m6 10v-7h6v7",
  agent: "M8 4h8l4 4v12H4V8l4-4m1 7h.01M15 11h.01M9 16h6M12 1v3",
  files: "M3 6h6l2 3h10v11H3V6m0 3V4h7l2 2h8v3",
  apps: "M3 3h7v7H3V3m11 0h7v7h-7V3M3 14h7v7H3v-7m11 0h7v7h-7v-7",
  docs: "M5 3h9l5 5v13H5V3m9 0v5h5M8 12h8M8 16h6",
  code: "m8 6-6 6 6 6m8-12 6 6-6 6M14 3l-4 18",
  browser: "M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0M3 12h18M12 3c5 5 5 13 0 18-5-5-5-13 0-18",
  settings: "M4 7h16M4 17h16M8 4v6m8 4v6",
  activity: "M2 12h5l3-8 4 16 3-8h5",
  devices: "M3 3h13v16H3V3m15 5h4v13h-8M7 16h5",
  search: "M16 10a6 6 0 1 1-12 0 6 6 0 0 1 12 0m-1 5 6 6",
  bell: "M6 8a6 6 0 0 1 12 0v5l2 4H4l2-4V8m4 12h4",
  close: "m6 6 12 12M6 18 18 6",
  minimize: "M5 17h14",
  maximize: "M5 5h14v14H5V5",
  restore: "M8 3h13v13M3 8h13v13H3V8",
  left: "M3 4h18v16H3V4m9 0v16M6 8h3m-3 4h3m-3 4h3",
  right: "M3 4h18v16H3V4m9 0v16m3-12h3m-3 4h3m-3 4h3",
  terminal: "m4 6 6 6-6 6m9 0h7",
  arrow: "M4 12h16m-6-6 6 6-6 6",
  chevron: "m7 10 5 5 5-5",
} as const;
export type IconName = keyof typeof paths;
export function Icon({ name, size = 20 }: { name: IconName; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name]} />
    </svg>
  );
}
