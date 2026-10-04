import { create } from 'zustand';

export interface TorrentJob {
  info_hash: string;
  name: string;
  magnet_uri: string;
  torrent_path: string;
  save_directory: string;
  status: 'downloading' | 'paused' | 'queued' | 'completed' | 'seeding' | 'error';
  total_bytes: number;
  downloaded: number;
  uploaded: number;
  progress: number;
  download_speed: number;
  upload_speed: number;
  peers: number;
  error_message: string;
  file_exists?: boolean;
  created_at: string;
  updated_at: string;
}

export interface LeechJob {
  id: string;
  url: string;
  filename: string;
  save_directory: string;
  total_bytes: number;
  downloaded: number;
  status: 'pending' | 'downloading' | 'paused' | 'completed' | 'error';
  progress: number;
  speed: number;
  threads: number;
  error_message: string;
  file_exists?: boolean;
  s3_stored?: boolean;
  created_at: string;
  updated_at: string;
}

export interface YouTubeJob {
  id: string;
  video_url: string;
  video_id: string;
  title: string;
  author: string;
  duration: string;
  duration_seconds: number;
  thumbnail: string;
  filename: string;
  save_directory: string;
  selected_itag: number;
  quality_label: string;
  mime_type: string;
  total_bytes: number;
  downloaded: number;
  status: 'pending' | 'fetching' | 'downloading' | 'converting' | 'completed' | 'error';
  progress: number;
  convert_progress: number;
  speed: number;
  convert_to_tv: boolean;
  convert_status: string;
  error_message: string;
  file_exists?: boolean;
  created_at: string;
  updated_at: string;
}

export interface SpotifyJob {
  id: string;
  spotify_url: string;
  spotify_id: string;
  title: string;
  artist: string;
  artists: string;
  album: string;
  album_artist: string;
  cover_url: string;
  release_date: string;
  track_number: number;
  total_tracks: number;
  duration_ms: number;
  isrc: string;
  genre: string;
  explicit: boolean;
  popularity: number;
  youtube_url: string;
  filename: string;
  save_directory: string;
  format: string;
  bitrate: string;
  total_bytes: number;
  downloaded: number;
  status: string;
  progress: number;
  speed: number;
  album_job_id: string;
  error_message: string;
  file_exists?: boolean;
  created_at: string;
  updated_at: string;
}

interface JobsState {
  torrents: TorrentJob[];
  leechJobs: LeechJob[];
  youtubeJobs: YouTubeJob[];
  spotifyJobs: SpotifyJob[];
  wsConnected: boolean;
  initWebSocket: (token: string) => () => void;
  sendAction: (cmd: { action: string; info_hash?: string; job_id?: string; delete_files?: boolean }) => void;
}

export const useJobsStore = create<JobsState>((set, get) => {
  let ws: WebSocket | null = null;
  let reconnectTimeout: any = null;

  return {
    torrents: [],
    leechJobs: [],
    youtubeJobs: [],
    spotifyJobs: [],
    wsConnected: false,

    initWebSocket: (token) => {
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${protocol}//${window.location.host}/ws/jobs?token=${token}`;

      const connect = () => {
        try {
          ws = new WebSocket(wsUrl);

          ws.onopen = () => {
            set({ wsConnected: true });
          };

          ws.onmessage = (event) => {
            try {
              const data = JSON.parse(event.data);
              set({
                torrents: data.torrents || [],
                leechJobs: data.leechJobs || [],
                youtubeJobs: data.youtubeJobs || [],
                spotifyJobs: data.spotifyJobs || [],
              });
            } catch (err) {
              console.error('Failed to parse jobs WS data', err);
            }
          };

          ws.onclose = () => {
            set({ wsConnected: false });
            // Reconnect after 3 seconds
            reconnectTimeout = setTimeout(connect, 3000);
          };

          ws.onerror = () => {
            set({ wsConnected: false });
          };
        } catch (e) {
          set({ wsConnected: false });
        }
      };

      connect();

      return () => {
        if (reconnectTimeout) clearTimeout(reconnectTimeout);
        if (ws) {
          ws.onclose = null;
          ws.close();
          ws = null;
        }
      };
    },

    sendAction: (cmd) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(cmd));
      } else {
        console.warn('Jobs WebSocket is not connected. Action ignored.', cmd);
      }
    }
  };
});
