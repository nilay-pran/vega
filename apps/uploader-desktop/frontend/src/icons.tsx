// Inline single-path SVGs: no icon dependency, and every glyph inherits the
// caller's stroke/fill classes.
type IconProps = { className?: string };

const stroke = (d: string, width = 1.5) =>
  function Icon({ className = "h-4 w-4" }: IconProps) {
    return (
      <svg
        viewBox="0 0 24 24"
        fill="none"
        className={`stroke-current ${className}`}
        strokeWidth={width}
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <path d={d} />
      </svg>
    );
  };

export const UploadCloudIcon = ({ className = "h-4 w-4" }: IconProps) => (
  <svg
    viewBox="0 0 24 24"
    fill="none"
    className={`stroke-current ${className}`}
    strokeWidth={1.5}
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <path d="M7 17.5A4.5 4.5 0 0 1 7.3 8.6a5.5 5.5 0 0 1 10.5 1.1A4 4 0 0 1 17.5 17.5" />
    <path d="M12 21V11m0 0 3 3m-3-3-3 3" />
  </svg>
);

export const PlusIcon = stroke("M12 5v14M5 12h14", 2);
export const SearchIcon = stroke("M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13Zm4.8-.2L20 21");
export const CancelIcon = stroke("M6 6l12 12M18 6 6 18", 2);
export const RetryIcon = stroke("M20 12a8 8 0 1 1-2.6-5.9M20 4v4h-4");
export const FolderIcon = stroke("M3 7.5A1.5 1.5 0 0 1 4.5 6h4l2 2.5h7A1.5 1.5 0 0 1 19 10v7.5A1.5 1.5 0 0 1 17.5 19h-13A1.5 1.5 0 0 1 3 17.5v-10Z");
export const GridIcon = stroke("M4 5h6v6H4V5Zm10 0h6v6h-6V5ZM4 13h6v6H4v-6Zm10 0h6v6h-6v-6Z");
export const ListIcon = stroke("M4 7h16M4 12h16M4 17h16", 2);
export const PulseIcon = stroke("M3 12h3.5l2.5-6 3 12 2.5-6H21", 1.8);
export const GearIcon = ({ className = "h-4 w-4" }: IconProps) => (
  <svg
    viewBox="0 0 24 24"
    fill="none"
    className={`stroke-current ${className}`}
    strokeWidth={1.5}
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <circle cx="12" cy="12" r="3" />
    <path d="M12 3v2.2M12 18.8V21M4.2 7.5l1.9 1.1M17.9 15.4l1.9 1.1M4.2 16.5l1.9-1.1M17.9 8.6l1.9-1.1" />
  </svg>
);

export const FilmIcon = ({ className = "h-4 w-4" }: IconProps) => (
  <svg
    viewBox="0 0 24 24"
    fill="none"
    className={`stroke-current ${className}`}
    strokeWidth={1.5}
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <rect x="3" y="4.5" width="18" height="15" rx="2" />
    <path d="M7.5 4.5v15M16.5 4.5v15M3 12h18M3 8.2h4.5M3 15.8h4.5M16.5 8.2H21M16.5 15.8H21" />
  </svg>
);

export const CheckIcon = stroke("M5 13l4.5 4.5L19 7", 2);
export const ClockIcon = ({ className = "h-4 w-4" }: IconProps) => (
  <svg
    viewBox="0 0 24 24"
    fill="none"
    className={`stroke-current ${className}`}
    strokeWidth={1.5}
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 7.5V12l3 2" />
  </svg>
);

export const PauseIcon = ({ className = "h-4 w-4" }: IconProps) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className}>
    <rect x="6" y="5" width="4" height="14" rx="1" />
    <rect x="14" y="5" width="4" height="14" rx="1" />
  </svg>
);

export const PlayIcon = ({ className = "h-4 w-4" }: IconProps) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className}>
    <path d="M7 5.5v13l11-6.5-11-6.5Z" />
  </svg>
);

export const FileIcon = ({ className = "h-5 w-5" }: IconProps) => (
  <svg
    viewBox="0 0 24 24"
    fill="none"
    className={`shrink-0 stroke-current ${className}`}
    strokeWidth={1.5}
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <path d="M6 3h7l5 5v11a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Z" />
    <path d="M13 3v5h5" />
  </svg>
);
