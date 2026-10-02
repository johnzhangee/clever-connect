import React, { useEffect, useState, useRef } from 'react';
import { useAuthStore } from '../store/authStore';
import { 
	FiFolder, FiFile, FiUpload, FiPlus, FiTrash2, FiDownload, 
	FiGrid, FiList, FiRefreshCw, FiArrowLeft, FiEdit3, 
	FiImage, FiVideo, FiZoomIn, FiZoomOut, FiRotateCw, FiX, FiCheck,
	FiChevronRight, FiChevronDown, FiScissors, FiCopy, FiClipboard, FiInfo, FiArchive, FiShare2,
	FiPlay, FiPause, FiMaximize2, FiChevronLeft, FiExternalLink,
	FiRotateCcw, FiVolume2, FiVolumeX, FiTv, FiMinimize2, FiEye, FiSend, FiSearch, FiCloud, FiUploadCloud
} from 'react-icons/fi';
import Editor from '@monaco-editor/react';
import { showGlobalAlert, showGlobalConfirm, showGlobalPrompt } from '../store/dialogStore';

interface FileItem {
	name: string;
	is_dir: boolean;
	size: number;
	mod_time: string;
	extension: string;
	s3_key?: string;
}

interface ClipboardState {
	action: 'copy' | 'cut' | null;
	srcParent: string;
	items: FileItem[];
}

// Helper functions defined outside component to avoid temporal dead zone (TDZ) initialization issues
const getFileCategory = (ext: string): 'image' | 'video' | 'code' | 'other' => {
	const e = ext.toLowerCase();
	if (['.png', '.jpg', '.jpeg', '.gif', '.svg', '.webp', '.ico'].includes(e)) return 'image';
	if (['.mp4', '.mkv', '.mov', '.webm', '.ogg', '.3gp'].includes(e)) return 'video';
	if (['.go', '.ts', '.tsx', '.js', '.jsx', '.css', '.scss', '.html', '.md', '.json', '.env', '.sh', '.yml', '.yaml', '.txt', '.ini', '.conf'].includes(e)) return 'code';
	return 'other';
};

const isArchiveFile = (fileName: string): boolean => {
	const lower = fileName.toLowerCase();
	if (lower.endsWith('.zip') || 
		lower.endsWith('.tar') || 
		lower.endsWith('.tar.gz') || 
		lower.endsWith('.tgz') || 
		lower.endsWith('.rar') || 
		lower.endsWith('.7z') || 
		lower.endsWith('.gz') || 
		lower.endsWith('.xz') || 
		lower.endsWith('.bz2') || 
		lower.endsWith('.zst')) {
		return true;
	}
	if (/\.part\d+$/i.test(lower) || /\.\d{3,}$/i.test(lower)) {
		return true;
	}
	return false;
};

const updateArchiveExtension = (name: string, ext: string): string => {
	const base = name.replace(/\.(zip|tar|tar\.gz|tgz|tar\.bz2|tbz2|tar\.xz|txz|tar\.zst|tzst|rar|7z)$/i, '');
	return base ? base + ext : 'archive' + ext;
};

const getMonacoLanguage = (ext: string): string => {
	const e = ext.toLowerCase();
	if (e === '.go') return 'go';
	if (e === '.ts' || e === '.tsx') return 'typescript';
	if (e === '.js' || e === '.jsx') return 'javascript';
	if (e === '.css') return 'css';
	if (e === '.scss') return 'scss';
	if (e === '.html') return 'html';
	if (e === '.md') return 'markdown';
	if (e === '.json') return 'json';
	if (e === '.sh') return 'shell';
	if (e === '.yml' || e === '.yaml') return 'yaml';
	return 'plaintext';
};

const formatDuration = (secs: number): string => {
	if (isNaN(secs)) return 'Unknown';
	const h = Math.floor(secs / 3600);
	const m = Math.floor((secs % 3600) / 60);
	const s = Math.floor(secs % 60);
	if (h > 0) {
		return `${h}h ${m}m ${s}s`;
	}
	return `${m}m ${s}s`;
};

const getResolutionLabel = (w: number, h: number): string => {
	if (w >= 3840 || h >= 2160) return '4K Ultra HD';
	if (w >= 2560 || h >= 1440) return '2K QHD';
	if (w >= 1920 || h >= 1080) return '1080p Full HD';
	if (w >= 1280 || h >= 720) return '720p HD';
	return 'SD';
};

const getFileIcon = (file: FileItem) => {
	if (file.is_dir) return <FiFolder style={{ color: '#eab308', fontSize: 36 }} />;
	const category = getFileCategory(file.extension);
	if (category === 'image') return <FiImage style={{ color: '#22c55e', fontSize: 36 }} />;
	if (category === 'video') return <FiVideo style={{ color: '#a855f7', fontSize: 36 }} />;
	if (category === 'code') return <FiEdit3 style={{ color: '#3b82f6', fontSize: 36 }} />;
	return <FiFile style={{ color: '#64748b', fontSize: 36 }} />;
};

const formatSize = (bytes: number): string => {
	if (bytes === 0) return '0 Bytes';
	const k = 1024;
	const sizes = ['Bytes', 'KB', 'MB', 'GB'];
	const i = Math.floor(Math.log(bytes) / Math.log(k));
	return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
};

interface CustomVideoPlayerProps {
	src: string;
	title: string;
	onClose: () => void;
}

const CustomVideoPlayer: React.FC<CustomVideoPlayerProps> = ({ src, title, onClose }) => {
	const videoRef = useRef<HTMLVideoElement | null>(null);
	const playerRef = useRef<HTMLDivElement | null>(null);
	const [isPlaying, setIsPlaying] = useState(false);
	const [currentTime, setCurrentTime] = useState(0);
	const [duration, setDuration] = useState(0);
	const [volume, setVolume] = useState(0); // 0 by default (muted)
	const [isMuted, setIsMuted] = useState(true);
	const [playbackSpeed, setPlaybackSpeed] = useState(1);
	const [isFullscreen, setIsFullscreen] = useState(false);
	const [showControls, setShowControls] = useState(true);
	const [isLoading, setIsLoading] = useState(true);
	const [hasError, setHasError] = useState(false);
	const controlsTimeoutRef = useRef<any>(null);

	// Sync play/pause state
	const togglePlay = () => {
		if (!videoRef.current) return;
		if (isPlaying) {
			videoRef.current.pause();
		} else {
			videoRef.current.play().catch(err => console.error("Play failed:", err));
		}
	};

	// Skip back/forward
	const skip = (seconds: number) => {
		if (!videoRef.current) return;
		videoRef.current.currentTime = Math.max(0, Math.min(videoRef.current.duration || 0, videoRef.current.currentTime + seconds));
	};

	// Format time helper (e.g. 1:05 or 0:35)
	const formatTime = (time: number) => {
		if (isNaN(time)) return '0:00';
		const mins = Math.floor(time / 60);
		const secs = Math.floor(time % 60);
		return `${mins}:${secs < 10 ? '0' : ''}${secs}`;
	};

	// Handle progress slider change
	const handleProgressChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		if (!videoRef.current) return;
		const newTime = parseFloat(e.target.value);
		videoRef.current.currentTime = newTime;
		setCurrentTime(newTime);
	};

	// Handle volume slider change
	const handleVolumeChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		if (!videoRef.current) return;
		const newVolume = parseFloat(e.target.value);
		setVolume(newVolume);
		videoRef.current.volume = newVolume;
		setIsMuted(newVolume === 0);
		videoRef.current.muted = newVolume === 0;
	};

	// Toggle Mute
	const toggleMute = () => {
		if (!videoRef.current) return;
		const nextMute = !isMuted;
		setIsMuted(nextMute);
		videoRef.current.muted = nextMute;
		if (!nextMute && volume === 0) {
			setVolume(0.5);
			videoRef.current.volume = 0.5;
		}
	};

	// Cycle playback speed
	const cycleSpeed = () => {
		if (!videoRef.current) return;
		const speeds = [0.5, 1, 1.25, 1.5, 2];
		const nextIdx = (speeds.indexOf(playbackSpeed) + 1) % speeds.length;
		const nextSpeed = speeds[nextIdx];
		setPlaybackSpeed(nextSpeed);
		videoRef.current.playbackRate = nextSpeed;
	};

	// Toggle Picture-in-Picture
	const togglePiP = async () => {
		if (!videoRef.current) return;
		try {
			if (document.pictureInPictureElement) {
				await document.exitPictureInPicture();
			} else {
				await videoRef.current.requestPictureInPicture();
			}
		} catch (err) {
			console.error("PiP error:", err);
		}
	};

	// Toggle Fullscreen
	const toggleFullscreen = () => {
		if (!playerRef.current) return;
		if (!document.fullscreenElement) {
			playerRef.current.requestFullscreen().catch(err => console.error("Fullscreen error:", err));
			setIsFullscreen(true);
		} else {
			document.exitFullscreen();
			setIsFullscreen(false);
		}
	};

	// Detect fullscreen change (e.g. Escape key pressed)
	useEffect(() => {
		const handleFsChange = () => {
			setIsFullscreen(!!document.fullscreenElement);
		};
		document.addEventListener('fullscreenchange', handleFsChange);
		return () => document.removeEventListener('fullscreenchange', handleFsChange);
	}, []);

	// Auto-hide controls loop
	const resetControlsTimeout = () => {
		setShowControls(true);
		if (controlsTimeoutRef.current) clearTimeout(controlsTimeoutRef.current);
		if (isPlaying) {
			controlsTimeoutRef.current = setTimeout(() => {
				setShowControls(false);
			}, 2500);
		}
	};

	useEffect(() => {
		resetControlsTimeout();
		return () => {
			if (controlsTimeoutRef.current) clearTimeout(controlsTimeoutRef.current);
		};
	}, [isPlaying]);

	// Handle video events
	const onPlay = () => setIsPlaying(true);
	const onPause = () => setIsPlaying(false);
	const onTimeUpdate = () => {
		if (videoRef.current) {
			setCurrentTime(videoRef.current.currentTime);
		}
	};
	const onDurationChange = () => {
		if (videoRef.current) {
			setDuration(videoRef.current.duration);
		}
	};
	const onWaiting = () => setIsLoading(true);
	const onPlaying = () => setIsLoading(false);
	const onError = () => {
		setIsLoading(false);
		setHasError(true);
	};

	// Keyboard shortcut listeners
	useEffect(() => {
		const handleKeyDown = (e: KeyboardEvent) => {
			if (document.activeElement?.tagName === 'INPUT' || document.activeElement?.tagName === 'TEXTAREA') {
				return;
			}
			switch (e.key.toLowerCase()) {
				case ' ':
					e.preventDefault();
					togglePlay();
					break;
				case 'arrowleft':
					e.preventDefault();
					skip(-10);
					break;
				case 'arrowright':
					e.preventDefault();
					skip(10);
					break;
				case 'arrowup':
					e.preventDefault();
					setVolume(prev => {
						const next = Math.min(1, prev + 0.1);
						if (videoRef.current) {
							videoRef.current.volume = next;
							videoRef.current.muted = false;
						}
						setIsMuted(false);
						return next;
					});
					break;
				case 'arrowdown':
					e.preventDefault();
					setVolume(prev => {
						const next = Math.max(0, prev - 0.1);
						if (videoRef.current) {
							videoRef.current.volume = next;
							if (next === 0) {
								videoRef.current.muted = true;
								setIsMuted(true);
							}
						}
						return next;
					});
					break;
				case 'f':
					e.preventDefault();
					toggleFullscreen();
					break;
				case 'm':
					e.preventDefault();
					toggleMute();
					break;
				case 'escape':
					if (!document.fullscreenElement) {
						onClose();
					}
					break;
			}
		};

		window.addEventListener('keydown', handleKeyDown);
		return () => window.removeEventListener('keydown', handleKeyDown);
	}, [isPlaying, isMuted, volume, playbackSpeed, onClose]);

	// Auto-mute sync on mount & clean up on unmount
	useEffect(() => {
		if (videoRef.current) {
			videoRef.current.muted = true;
			videoRef.current.volume = 0;
		}
		return () => {
			if (videoRef.current) {
				try {
					videoRef.current.pause();
					videoRef.current.src = "";
					videoRef.current.load();
				} catch (e) {
					console.error("Failed to load blank src:", e);
				}
			}
		};
	}, [src]);

	return (
		<div 
			ref={playerRef}
			className="custom-video-player"
			style={{
				position: 'relative',
				width: '100%',
				height: '100%',
				background: '#000',
				overflow: 'hidden',
				display: 'flex',
				alignItems: 'center',
				justifyContent: 'center',
				userSelect: 'none',
				fontFamily: 'system-ui, -apple-system, sans-serif'
			}}
			onMouseMove={resetControlsTimeout}
			onMouseLeave={() => isPlaying && setShowControls(false)}
		>
			<video
				ref={videoRef}
				src={src}
				onClick={togglePlay}
				onDoubleClick={toggleFullscreen}
				onPlay={onPlay}
				onPause={onPause}
				onTimeUpdate={onTimeUpdate}
				onDurationChange={onDurationChange}
				onWaiting={onWaiting}
				onPlaying={onPlaying}
				onError={onError}
				autoPlay
				style={{
					width: '100%',
					height: '100%',
					maxHeight: '100%',
					objectFit: 'contain',
					cursor: 'pointer'
				}}
			/>

			{isLoading && (
				<div style={{ position: 'absolute', display: 'flex', alignItems: 'center', justifyContent: 'center', pointerEvents: 'none' }}>
					<div className="spin-anim" style={{ width: 48, height: 48, border: '4px solid rgba(255,255,255,0.2)', borderTopColor: '#fff', borderRadius: '50%' }} />
				</div>
			)}

			{hasError && (
				<div style={{ position: 'absolute', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.9)', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: 20, textAlign: 'center', color: '#fff' }}>
					<FiVideo size={48} style={{ color: '#ef4444', marginBottom: 12 }} />
					<h3 style={{ fontSize: 16, fontWeight: 700, margin: '0 0 8px 0' }}>Unsupported Video Format or Network Failure</h3>
					<p style={{ fontSize: 13, color: '#aaa', maxWidth: 400, margin: '0 0 16px 0' }}>The browser cannot play this video. It may be using an unsupported codec (like HEVC/H.265 or AC3 audio). You can download the file to play it locally.</p>
					<button onClick={() => { setHasError(false); setIsLoading(true); if (videoRef.current) { videoRef.current.load(); } }} className="btn btn--primary" style={{ padding: '8px 16px', fontSize: 12 }}>
						Retry Playback
					</button>
				</div>
			)}

			<div 
				style={{
					position: 'absolute',
					top: 0,
					left: 0,
					right: 0,
					padding: '20px 24px 40px 24px',
					background: 'linear-gradient(to bottom, rgba(0,0,0,0.7) 0%, rgba(0,0,0,0) 100%)',
					display: 'flex',
					justifyContent: 'space-between',
					alignItems: 'center',
					transition: 'opacity 0.3s ease, transform 0.3s ease',
					opacity: showControls ? 1 : 0,
					transform: showControls ? 'translateY(0)' : 'translateY(-10px)',
					pointerEvents: showControls ? 'auto' : 'none'
				}}
			>
				<span style={{ color: '#fff', fontSize: 14, fontWeight: 600, textShadow: '0 1px 3px rgba(0,0,0,0.6)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '80%' }}>
					{title}
				</span>
				<button 
					onClick={onClose}
					style={{
						background: 'rgba(255,255,255,0.1)',
						border: 'none',
						color: '#fff',
						padding: 8,
						borderRadius: '50%',
						cursor: 'pointer',
						display: 'flex',
						alignItems: 'center',
						justifyContent: 'center',
						backdropFilter: 'blur(4px)',
						transition: 'background 0.2s'
					}}
					onMouseEnter={(e) => e.currentTarget.style.background = 'rgba(255,255,255,0.2)'}
					onMouseLeave={(e) => e.currentTarget.style.background = 'rgba(255,255,255,0.1)'}
				>
					<FiX size={18} />
				</button>
			</div>

			{showControls && !isLoading && !hasError && (
				<div 
					onClick={togglePlay}
					style={{
						position: 'absolute',
						cursor: 'pointer',
						width: 72,
						height: 72,
						borderRadius: '50%',
						background: 'rgba(0,0,0,0.5)',
						border: '1px solid rgba(255,255,255,0.15)',
						display: 'flex',
						alignItems: 'center',
						justifyContent: 'center',
						color: '#fff',
						backdropFilter: 'blur(8px)',
						transition: 'transform 0.2s, opacity 0.2s',
						opacity: 0.95
					}}
					onMouseEnter={(e) => e.currentTarget.style.transform = 'scale(1.1)'}
					onMouseLeave={(e) => e.currentTarget.style.transform = 'scale(1)'}
				>
					{isPlaying ? <FiPause size={28} /> : <FiPlay size={28} style={{ marginLeft: 4 }} />}
				</div>
			)}

			<div 
				style={{
					position: 'absolute',
					bottom: 16,
					left: 16,
					right: 16,
					height: 48,
					background: 'rgba(15, 15, 15, 0.65)',
					backdropFilter: 'blur(16px)',
					border: '1px solid rgba(255,255,255,0.08)',
					borderRadius: 30,
					display: 'flex',
					alignItems: 'center',
					padding: '0 16px',
					gap: 12,
					color: '#fff',
					transition: 'opacity 0.3s ease, transform 0.3s ease',
					opacity: showControls ? 1 : 0,
					transform: showControls ? 'translateY(0)' : 'translateY(10px)',
					pointerEvents: showControls ? 'auto' : 'none',
					boxShadow: '0 10px 30px rgba(0,0,0,0.5)'
				}}
				onClick={(e) => e.stopPropagation()}
			>
				<button 
					onClick={togglePlay} 
					style={{ background: 'none', border: 'none', color: 'inherit', cursor: 'pointer', display: 'flex', alignItems: 'center', padding: 4 }}
				>
					{isPlaying ? <FiPause size={16} /> : <FiPlay size={16} />}
				</button>

				<button 
					onClick={() => skip(-10)} 
					title="Back 10s"
					style={{ background: 'none', border: 'none', color: 'inherit', cursor: 'pointer', display: 'flex', alignItems: 'center', padding: 4 }}
				>
					<FiRotateCcw size={16} />
				</button>

				<button 
					onClick={() => skip(10)} 
					title="Forward 10s"
					style={{ background: 'none', border: 'none', color: 'inherit', cursor: 'pointer', display: 'flex', alignItems: 'center', padding: 4 }}
				>
					<FiRotateCw size={16} />
				</button>

				<span style={{ fontSize: 12, fontWeight: 500, minWidth: 32, textAlign: 'center', opacity: 0.9, fontFamily: 'monospace' }}>
					{formatTime(currentTime)}
				</span>

				<div style={{ flex: 1, display: 'flex', alignItems: 'center', position: 'relative' }}>
					<input 
						type="range"
						min={0}
						max={duration || 100}
						value={currentTime}
						onChange={handleProgressChange}
						className="video-scrubber"
						style={{
							width: '100%',
							cursor: 'pointer',
							height: 4,
							borderRadius: 2,
							background: `linear-gradient(to right, var(--color-brand, #ea580c) 0%, var(--color-brand, #ea580c) ${(currentTime / (duration || 1)) * 100}%, rgba(255,255,255,0.2) ${(currentTime / (duration || 1)) * 100}%, rgba(255,255,255,0.2) 100%)`,
							outline: 'none',
							WebkitAppearance: 'none',
						}}
					/>
				</div>

				<span style={{ fontSize: 12, fontWeight: 500, minWidth: 32, textAlign: 'center', opacity: 0.9, fontFamily: 'monospace' }}>
					{formatTime(duration)}
				</span>

				<button 
					onClick={cycleSpeed}
					title="Playback Speed"
					style={{
						background: 'rgba(255,255,255,0.08)',
						border: '1px solid rgba(255,255,255,0.1)',
						borderRadius: 6,
						color: 'inherit',
						fontSize: 11,
						fontWeight: 600,
						padding: '2px 6px',
						cursor: 'pointer',
						display: 'flex',
						alignItems: 'center',
						minWidth: 28,
						justifyContent: 'center'
					}}
				>
					{playbackSpeed}x
				</button>

				<div 
					className="volume-control-container"
					style={{ display: 'flex', alignItems: 'center', gap: 6, position: 'relative' }}
				>
					<button 
						onClick={toggleMute}
						style={{ background: 'none', border: 'none', color: 'inherit', cursor: 'pointer', display: 'flex', alignItems: 'center', padding: 4 }}
					>
						{isMuted || volume === 0 ? <FiVolumeX size={16} /> : <FiVolume2 size={16} />}
					</button>
					<input 
						type="range"
						min={0}
						max={1}
						step={0.05}
						value={isMuted ? 0 : volume}
						onChange={handleVolumeChange}
						className="volume-slider"
						style={{
							width: 60,
							cursor: 'pointer',
							height: 4,
							borderRadius: 2,
							background: `linear-gradient(to right, #fff 0%, #fff ${(isMuted ? 0 : volume) * 100}%, rgba(255,255,255,0.2) ${(isMuted ? 0 : volume) * 100}%, rgba(255,255,255,0.2) 100%)`,
							outline: 'none',
							WebkitAppearance: 'none'
						}}
					/>
				</div>

				<button 
					onClick={togglePiP}
					title="Picture in Picture"
					style={{ background: 'none', border: 'none', color: 'inherit', cursor: 'pointer', display: 'flex', alignItems: 'center', padding: 4 }}
				>
					<FiTv size={16} />
				</button>

				<button 
					onClick={toggleFullscreen}
					title="Fullscreen"
					style={{ background: 'none', border: 'none', color: 'inherit', cursor: 'pointer', display: 'flex', alignItems: 'center', padding: 4 }}
				>
					{isFullscreen ? <FiMinimize2 size={16} /> : <FiMaximize2 size={16} />}
				</button>
			</div>

			<style>{`
				.video-scrubber::-webkit-slider-thumb {
					-webkit-appearance: none;
					appearance: none;
					width: 10px;
					height: 10px;
					border-radius: 50%;
					background: #fff;
					transition: transform 0.1s;
				}
				.video-scrubber:hover::-webkit-slider-thumb {
					transform: scale(1.3);
				}
				.volume-slider::-webkit-slider-thumb {
					-webkit-appearance: none;
					appearance: none;
					width: 8px;
					height: 8px;
					border-radius: 50%;
					background: #fff;
				}
			`}</style>
		</div>
	);
};

export const FilesPage: React.FC = () => {
	const { token } = useAuthStore();
	const [currentPath, setCurrentPath] = useState<string>('/');
	const [files, setFiles] = useState<FileItem[]>([]);
	const [loading, setLoading] = useState<boolean>(false);
	const [viewMode, setViewMode] = useState<'grid' | 'list'>('grid');
	const [searchQuery, setSearchQuery] = useState<string>('');
	const [sortBy, setSortBy] = useState<'name' | 'size' | 'time' | 'type'>('name');
	const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('asc');

	// Context Menu State
	const [contextMenu, setContextMenu] = useState<{
		visible: boolean;
		x: number;
		y: number;
		file: FileItem | null;
	}>({ visible: false, x: 0, y: 0, file: null });

	useEffect(() => {
		const handleWindowClick = () => {
			setContextMenu(prev => prev.visible ? { ...prev, visible: false } : prev);
		};
		window.addEventListener('click', handleWindowClick);
		return () => window.removeEventListener('click', handleWindowClick);
	}, []);

	// Multi-Selection State
	const [selectedItems, setSelectedItems] = useState<string[]>([]);

	// Clipboard / Pasteboard State (Cut, Copy, Paste)
	const [clipboard, setClipboard] = useState<ClipboardState>({ action: null, srcParent: '', items: [] });

	// Interactive Tree Folders List (loaded dynamically for structural sidebar)
	const [folderTree, setFolderTree] = useState<string[]>([]);

	// Server machine HDD info
	const [diskTotal, setDiskTotal] = useState<number>(0);
	const [diskFree, setDiskFree] = useState<number>(0);
	const [diskUsed, setDiskUsed] = useState<number>(0);
	const [s3Enabled, setS3Enabled] = useState<boolean>(false);
	const [s3ObjectCount, setS3ObjectCount] = useState<number>(0);
	const [s3TotalSize, setS3TotalSize] = useState<number>(0);

	// Lightbox Gallery States
	const [isLightboxOpen, setIsLightboxOpen] = useState<boolean>(false);
	const [lightboxIndex, setLightboxIndex] = useState<number>(0);
	const [isPlaying, setIsPlaying] = useState<boolean>(false);

	const imageFiles = files.filter(f => getFileCategory(f.extension) === 'image' && !f.is_dir);

	// Auto-play Slideshow handler
	useEffect(() => {
		let timer: any;
		if (isPlaying && isLightboxOpen && imageFiles.length > 1) {
			timer = setInterval(() => {
				setLightboxIndex(prev => (prev + 1) % imageFiles.length);
			}, 3000);
		}
		return () => {
			if (timer) clearInterval(timer);
		};
	}, [isPlaying, isLightboxOpen, imageFiles.length]);

	// Keyboard Navigation for Lightbox Gallery
	useEffect(() => {
		const handleKeyDown = (e: KeyboardEvent) => {
			if (!isLightboxOpen || imageFiles.length === 0) return;
			if (e.key === 'ArrowRight') {
				setLightboxIndex(prev => (prev + 1) % imageFiles.length);
				setIsPlaying(false);
			} else if (e.key === 'ArrowLeft') {
				setLightboxIndex(prev => (prev - 1 + imageFiles.length) % imageFiles.length);
				setIsPlaying(false);
			} else if (e.key === 'Escape') {
				setIsLightboxOpen(false);
				setIsPlaying(false);
			} else if (e.key === ' ') {
				e.preventDefault();
				setIsPlaying(prev => !prev);
			}
		};
		window.addEventListener('keydown', handleKeyDown);
		return () => window.removeEventListener('keydown', handleKeyDown);
	}, [isLightboxOpen, imageFiles.length]);

	// Active Preview States
	const [previewFile, setPreviewFile] = useState<FileItem | null>(null);
	const [previewType, setPreviewType] = useState<'image' | 'video' | 'editor' | 'other' | null>(null);

	// Modals and inputs
	const [showFolderModal, setShowFolderModal] = useState<boolean>(false);
	const [newFolderName, setNewFolderName] = useState<string>('');
	const [creatingFolder, setCreatingFolder] = useState<boolean>(false);

	// Cloud Storage (rclone) Upload Modal
	const [showCloudUploadModal, setShowCloudUploadModal] = useState<boolean>(false);
	const [cloudRemotes, setCloudRemotes] = useState<{ id: number; name: string; type: string }[]>([]);
	const [cloudRemoteId, setCloudRemoteId] = useState<string>('');
	const [cloudDestDir, setCloudDestDir] = useState<string>('');
	const [cloudKeepStructure, setCloudKeepStructure] = useState<boolean>(true);
	const [cloudNeedLink, setCloudNeedLink] = useState<boolean>(false);
	const [cloudUploading, setCloudUploading] = useState<boolean>(false);

	// Archive Modal
	const [showArchiveModal, setShowArchiveModal] = useState<boolean>(false);
	const [archiveName, setArchiveName] = useState<string>('archive.zip');
	const [archiveFormat, setArchiveFormat] = useState<string>('.zip');
	const [compressing, setCompressing] = useState<boolean>(false);
	const [compressQueue, setCompressQueue] = useState<{ name: string; path: string }[]>([]);
	const [showCompressQueueModal, setShowCompressQueueModal] = useState<boolean>(false);

	// Archive Password Modal
	const [passwordModal, setPasswordModal] = useState<{ show: boolean, targetPath: string, callback?: (password: string) => void }>({ show: false, targetPath: '' });
	const [archivePassword, setArchivePassword] = useState<string>('');

	// Share Modal
	const [showShareModal, setShowShareModal] = useState<boolean>(false);
	const [shareUrl, setShareUrl] = useState<string>('');

	// Text/Code Editor States
	const [editorContent, setEditorContent] = useState<string>('');
	const [editorLoading, setEditorLoading] = useState<boolean>(false);
	const [savingContent, setSavingContent] = useState<boolean>(false);
	const [editorLang, setEditorLang] = useState<string>('plaintext');

	// Image control state
	const [imgZoom, setImgZoom] = useState<number>(1);
	const [imgRotation, setImgRotation] = useState<number>(0);

	// Video Cinema Modal and Metadata Detector States
	const [showVideoModal, setShowVideoModal] = useState<boolean>(false);
	const [videoModalFile, setVideoModalFile] = useState<FileItem | null>(null);
	const [detectedMetadata, setDetectedMetadata] = useState<{ width: number; height: number; duration: number } | null>(null);

	// Upload State
	const [uploading, setUploading] = useState<boolean>(false);
	const fileInputRef = useRef<HTMLInputElement>(null);

	// Global Physical Search Modal States
	const [showSearchModal, setShowSearchModal] = useState<boolean>(false);
	const [globalSearchQuery, setGlobalSearchQuery] = useState<string>('');
	const [globalSearchResults, setGlobalSearchResults] = useState<(FileItem & { path: string })[]>([]);
	const [globalSearchLoading, setGlobalSearchLoading] = useState<boolean>(false);

	useEffect(() => {
		if (!showSearchModal) {
			setGlobalSearchQuery('');
			setGlobalSearchResults([]);
			return;
		}
	}, [showSearchModal]);

	useEffect(() => {
		if (globalSearchQuery.trim().length <= 3) {
			setGlobalSearchResults([]);
			return;
		}

		const delayDebounce = setTimeout(async () => {
			setGlobalSearchLoading(true);
			try {
				const res = await fetch(`/api/files/search?path=${encodeURIComponent(currentPath)}&q=${encodeURIComponent(globalSearchQuery.trim())}`, {
					headers: {
						'Authorization': `Bearer ${token}`
					}
				});
				if (res.ok) {
					const data = await res.json();
					setGlobalSearchResults(data || []);
				}
			} catch (err) {
				console.error("Global search failed:", err);
			} finally {
				setGlobalSearchLoading(false);
			}
		}, 300);

		return () => clearTimeout(delayDebounce);
	}, [globalSearchQuery, currentPath, token]);

	// Fetch current directory contents
	const fetchFiles = async (path: string) => {
		setLoading(true);
		setSelectedItems([]);
		try {
			const res = await fetch(`/api/files/list?path=${encodeURIComponent(path)}`, {
				headers: { 'Authorization': `Bearer ${token}` }
			});
			if (res.ok) {
				const data = await res.json();
				setFiles(data.files || []);
				setCurrentPath(data.current_path || '/');
				setDiskTotal(data.disk_total || 0);
				setDiskFree(data.disk_free || 0);
				setDiskUsed(data.disk_used || 0);
				setS3Enabled(data.s3_enabled || false);
				setS3ObjectCount(data.s3_object_count || 0);
				setS3TotalSize(data.s3_total_size || 0);
				
				// Update structural sidebar helper
				updateFolderTree(data.current_path || '/', data.files || []);
			}
		} catch (err) {
			console.error("Failed to load directory files", err);
		} finally {
			setLoading(false);
		}
	};

	// Keep unique set of discovered folder paths to construct directory tree
	const updateFolderTree = (current: string, currentFiles: FileItem[]) => {
		setFolderTree(prev => {
			const foldersSet = new Set(prev);
			foldersSet.add('/');
			if (current !== '/' && current !== '') {
				foldersSet.add(current);
			}
			currentFiles.forEach(f => {
				if (f.is_dir) {
					const full = current === '/' ? `/${f.name}` : `${current}/${f.name}`;
					foldersSet.add(full);
				}
			});
			return Array.from(foldersSet).sort();
		});
	};

	useEffect(() => {
		if (token) {
			fetchFiles(currentPath);
		}
	}, [currentPath, token]);

	// Navigate path helpers
	const navigateTo = (name: string) => {
		const newPath = currentPath === '/' ? `/${name}` : `${currentPath}/${name}`;
		setCurrentPath(newPath);
	};

	const navigateUp = () => {
		if (currentPath === '/' || currentPath === '') return;
		const parts = currentPath.split('/');
		parts.pop();
		const parent = parts.join('/') || '/';
		setCurrentPath(parent);
	};

	const navigateToBreadcrumb = (index: number, parts: string[]) => {
		const target = '/' + parts.slice(0, index + 1).join('/');
		setCurrentPath(target);
	};

	// Selection handlers
	const toggleSelectItem = (name: string) => {
		setSelectedItems(prev => 
			prev.includes(name) ? prev.filter(x => x !== name) : [...prev, name]
		);
	};

	const toggleSelectAll = () => {
		if (selectedItems.length === filteredFiles.length) {
			setSelectedItems([]);
		} else {
			setSelectedItems(filteredFiles.map(x => x.name));
		}
	};

	// Sorting modules
	const sortedFiles = [...files].sort((a, b) => {
		// Keep directories on top
		if (a.is_dir && !b.is_dir) return -1;
		if (!a.is_dir && b.is_dir) return 1;

		let valA: any = a.name.toLowerCase();
		let valB: any = b.name.toLowerCase();

		if (sortBy === 'size') {
			valA = a.size;
			valB = b.size;
		} else if (sortBy === 'time') {
			valA = new Date(a.mod_time).getTime();
			valB = new Date(b.mod_time).getTime();
		} else if (sortBy === 'type') {
			valA = a.extension.toLowerCase();
			valB = b.extension.toLowerCase();
		}

		if (valA < valB) return sortOrder === 'asc' ? -1 : 1;
		if (valA > valB) return sortOrder === 'asc' ? 1 : -1;
		return 0;
	});

	const filteredFiles = sortedFiles.filter(f => f.name.toLowerCase().includes(searchQuery.toLowerCase()));

	// Keyboard shortcut: Ctrl+A / Cmd+A to select all files in the current folder
	useEffect(() => {
		const handleKeyDown = (e: KeyboardEvent) => {
			// Don't intercept Ctrl+A if user is typing in form inputs, textareas, code editors, etc.
			const target = e.target as HTMLElement;
			if (
				target.tagName === 'INPUT' ||
				target.tagName === 'TEXTAREA' ||
				target.tagName === 'SELECT' ||
				target.isContentEditable ||
				target.closest('.monaco-editor') ||
				target.closest('.cm-editor')
			) {
				return;
			}

			if ((e.ctrlKey || e.metaKey) && (e.key === 'a' || e.key === 'A')) {
				e.preventDefault();
				setSelectedItems(filteredFiles.map(x => x.name));
			}
		};

		window.addEventListener('keydown', handleKeyDown);
		return () => {
			window.removeEventListener('keydown', handleKeyDown);
		};
	}, [filteredFiles]);

	// Clipboard / Pasteboard actions
	const handleCopy = () => {
		if (selectedItems.length === 0) return;
		const itemsToCopy = files.filter(x => selectedItems.includes(x.name));
		setClipboard({ action: 'copy', srcParent: currentPath, items: itemsToCopy });
		showGlobalAlert(`Copied ${selectedItems.length} items to clipboard.`, { title: 'Copied', variant: 'success' });
	};

	const handleCut = () => {
		if (selectedItems.length === 0) return;
		const itemsToCut = files.filter(x => selectedItems.includes(x.name));
		setClipboard({ action: 'cut', srcParent: currentPath, items: itemsToCut });
		showGlobalAlert(`Cut ${selectedItems.length} items to clipboard.`, { title: 'Cut', variant: 'success' });
	};

	const handlePaste = async () => {
		if (!clipboard.action || clipboard.items.length === 0) return;
		setLoading(true);
		try {
			for (const item of clipboard.items) {
				const src = clipboard.srcParent === '/' ? `/${item.name}` : `${clipboard.srcParent}/${item.name}`;
				const dst = currentPath === '/' ? `/${item.name}` : `${currentPath}/${item.name}`;

				const endpoint = clipboard.action === 'copy' ? '/api/files/copy' : '/api/files/move';
				await fetch(endpoint, {
					method: 'POST',
					headers: {
						'Content-Type': 'application/json',
						'Authorization': `Bearer ${token}`
					},
					body: JSON.stringify({ src_path: src, dst_path: dst })
				});
			}

			// Clear clipboard if cut action completed
			if (clipboard.action === 'cut') {
				setClipboard({ action: null, srcParent: '', items: [] });
			}
			fetchFiles(currentPath);
		} catch (err) {
			console.error(err);
		} finally {
			setLoading(false);
		}
	};

	// Rename item
	const handleRename = async (file: FileItem) => {
		const newName = await showGlobalPrompt(`Enter new name for "${file.name}":`, file.name, { title: 'Rename Item' });
		if (!newName || newName.trim() === '' || newName === file.name) return;

		const src = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
		const dst = currentPath === '/' ? `/${newName.trim()}` : `${currentPath}/${newName.trim()}`;

		setLoading(true);
		try {
			const res = await fetch('/api/files/move', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({ src_path: src, dst_path: dst })
			});
			if (res.ok) {
				fetchFiles(currentPath);
			}
		} catch (err) {
			console.error(err);
		} finally {
			setLoading(false);
		}
	};

	// Zip compression
	const handleCompress = async (e: React.FormEvent) => {
		e.preventDefault();
		if (selectedItems.length === 0 || !archiveName.trim()) return;
		setCompressing(true);
		try {
			const res = await fetch('/api/files/compress', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({
					parent_path: currentPath,
					items: selectedItems,
					zip_name: archiveName.trim()
				})
			});
			if (res.ok) {
				const data = await res.json();
				showGlobalAlert(`Compression job queued successfully! Job ID: ${data.job_id}`, { title: 'Compression Queued', variant: 'success' });
				setShowArchiveModal(false);
				fetchFiles(currentPath);
			} else {
				const errData = await res.json();
				showGlobalAlert(`Compression failed: ${errData.error}`, { title: 'Compression Failed', variant: 'error' });
			}
		} catch (err) {
			console.error(err);
		} finally {
			setCompressing(false);
		}
	};

	// Zip Extraction
	const handleExtract = async (file: FileItem) => {
		if (!(await showGlobalConfirm(`Do you want to extract "${file.name}" in the current directory?`, { title: 'Extract Archive', variant: 'question' }))) return;
		const targetPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
		setLoading(true);
		try {
			const res = await fetch('/api/files/decompress', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({ path: targetPath })
			});
			if (res.ok) {
				const data = await res.json();
				if (data.status === 'password_required') {
					setPasswordModal({
						show: true,
						targetPath: targetPath,
						callback: async (pwd: string) => {
							setLoading(true);
							try {
								const finalRes = await fetch('/api/files/decompress', {
									method: 'POST',
									headers: {
										'Content-Type': 'application/json',
										'Authorization': `Bearer ${token}`
									},
									body: JSON.stringify({ path: targetPath, password: pwd })
								});
								const finalData = await finalRes.json();
								if (finalRes.ok && finalData.status === 'success') {
									showGlobalAlert(`Extraction job queued successfully! Job ID: ${finalData.job_id}`, { title: 'Extraction Queued', variant: 'success' });
									fetchFiles(currentPath);
								} else {
									showGlobalAlert(`Extraction failed: ${finalData.error || 'Wrong password or corrupt archive'}`, { title: 'Extraction Failed', variant: 'error' });
								}
							} catch (e) {
								console.error(e);
							} finally {
								setLoading(false);
							}
						}
					});
					return;
				}
				showGlobalAlert(`Extraction job queued successfully! Job ID: ${data.job_id}`, { title: 'Extraction Queued', variant: 'success' });
				fetchFiles(currentPath);
			} else {
				const errData = await res.json();
				showGlobalAlert(`Extraction failed: ${errData.error}`, { title: 'Extraction Failed', variant: 'error' });
			}
		} catch (err) {
			console.error(err);
		} finally {
			setLoading(false);
		}
	};

	const handleAddToCompressQueue = (file: FileItem) => {
		const fullPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
		setCompressQueue(prev => {
			if (prev.some(item => item.path === fullPath)) {
				showGlobalAlert(`"${file.name}" is already in the compress queue.`, { title: 'Already Queued', variant: 'info' });
				return prev;
			}
			return [...prev, { name: file.name, path: fullPath }];
		});
	};

	const handleAddSelectedToCompressQueue = () => {
		setCompressQueue(prev => {
			const newItems = [...prev];
			let addedCount = 0;
			selectedItems.forEach(name => {
				const file = files.find(f => f.name === name);
				if (file) {
					const fullPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
					if (!newItems.some(item => item.path === fullPath)) {
						newItems.push({ name: file.name, path: fullPath });
						addedCount++;
					}
				}
			});
			if (addedCount > 0) {
				showGlobalAlert(`Added ${addedCount} items to the compress queue.`, { title: 'Added to Queue', variant: 'success' });
			}
			return newItems;
		});
		setSelectedItems([]);
	};

	const handleCompressQueue = async () => {
		if (compressQueue.length === 0 || !archiveName.trim()) return;
		setCompressing(true);
		try {
			const res = await fetch('/api/files/compress', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({
					parent_path: '',
					items: compressQueue.map(item => item.path),
					zip_name: archiveName.trim()
				})
			});
			if (res.ok) {
				const data = await res.json();
				showGlobalAlert(`Compression job queued successfully for all queue items! Job ID: ${data.job_id}`, { title: 'Compression Queued', variant: 'success' });
				setCompressQueue([]);
				setShowCompressQueueModal(false);
				fetchFiles(currentPath);
			} else {
				const errData = await res.json();
				showGlobalAlert(`Compression failed: ${errData.error}`, { title: 'Compression Failed', variant: 'error' });
			}
		} catch (err) {
			console.error(err);
		} finally {
			setCompressing(false);
		}
	};

	// Dynamic Share URL generator
	const handleShareLink = (file: FileItem) => {
		const targetPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
		const absoluteLink = `${window.location.origin}/api/files/stream?path=${encodeURIComponent(targetPath)}&token=${encodeURIComponent(token || '')}`;
		setShareUrl(absoluteLink);
		setShowShareModal(true);
	};

	// Context menu handlers
	const handleContextMenu = (e: React.MouseEvent, file: FileItem) => {
		e.preventDefault();
		if (!selectedItems.includes(file.name)) {
			setSelectedItems([file.name]);
		}
		setContextMenu({
			visible: true,
			x: e.clientX,
			y: e.clientY,
			file
		});
	};

	const handleSendToTelegram = async (file: FileItem) => {
		const targetPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
		try {
			const res = await fetch('/api/telegram/send-file', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({ file_path: targetPath })
			});
			if (res.ok) {
				const data = await res.json();
				showGlobalAlert(`Upload job queued in scheduler! Job ID: ${data.job_id}`, { title: 'Upload Queued', variant: 'success' });
			} else {
				const errData = await res.json();
				showGlobalAlert(`Failed to send to Telegram: ${errData.error}`, { title: 'Upload Failed', variant: 'error' });
			}
		} catch (err: any) {
			showGlobalAlert(`Error sending file to Telegram: ${err.message}`, { title: 'Error', variant: 'error' });
		}
	};

	const handleCopySingle = (file: FileItem) => {
		setClipboard({ action: 'copy', srcParent: currentPath, items: [file] });
	};

	const handleCutSingle = (file: FileItem) => {
		setClipboard({ action: 'cut', srcParent: currentPath, items: [file] });
	};

	// Move a single local file to S3 storage (cut: upload to S3 + delete local).
	// Runs as a scheduler job so it is cancellable, retryable and visible.
	const handleMoveToS3 = async (file: FileItem) => {
		const targetPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
		try {
			const res = await fetch('/api/files/to-s3', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
				body: JSON.stringify({ paths: [targetPath] })
			});
			if (res.ok) {
				const data = await res.json();
				showGlobalAlert(`Move to S3 job queued! Job ID: ${data.job_id}`, { title: 'Move to S3 Queued', variant: 'success' });
				fetchFiles(currentPath);
			} else {
				const errData = await res.json();
				showGlobalAlert(`Failed to move to S3: ${errData.error}`, { title: 'Move to S3 Failed', variant: 'error' });
			}
		} catch (err: any) {
			showGlobalAlert(`Error moving to S3: ${err.message}`, { title: 'Error', variant: 'error' });
		}
	};

	// Bulk move selected items (files and/or folders) to S3 storage in a single job.
	const handleMoveToS3Bulk = async () => {
		if (selectedItems.length === 0) return;
		const paths = selectedItems.map(name => currentPath === '/' ? `/${name}` : `${currentPath}/${name}`);
		try {
			const res = await fetch('/api/files/to-s3', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
				body: JSON.stringify({ paths })
			});
			if (res.ok) {
				const data = await res.json();
				showGlobalAlert(`Move to S3 job queued for ${selectedItems.length} items! Job ID: ${data.job_id}`, { title: 'Bulk Move to S3 Queued', variant: 'success' });
				setSelectedItems([]);
				fetchFiles(currentPath);
			} else {
				const errData = await res.json();
				showGlobalAlert(`Failed to move to S3: ${errData.error}`, { title: 'Move to S3 Failed', variant: 'error' });
			}
		} catch (err: any) {
			showGlobalAlert(`Error moving to S3: ${err.message}`, { title: 'Error', variant: 'error' });
		}
	};

	// Open the "Upload to Cloud" modal: loads the admin's enabled rclone remotes.
	const openCloudUploadModal = async () => {
		if (selectedItems.length === 0) return;
		try {
			const res = await fetch('/api/rclone/remotes', {
				headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` }
			});
			if (res.ok) {
				const data = await res.json();
				const enabled = (data.remotes || []).filter((r: any) => r.enabled);
				if (enabled.length === 0) {
					showGlobalAlert('No enabled cloud remotes — configure one on the Cloud Storage page first.', { title: 'Cloud Upload', variant: 'warning' });
					return;
				}
				setCloudRemotes(enabled);
				setCloudRemoteId(String(enabled[0].id));
				setCloudDestDir('');
				setCloudKeepStructure(true);
				setCloudNeedLink(false);
				setShowCloudUploadModal(true);
			} else {
				showGlobalAlert('Cloud storage is unavailable on this server.', { title: 'Cloud Upload', variant: 'warning' });
			}
		} catch (err: any) {
			showGlobalAlert(`Error loading remotes: ${err.message}`, { title: 'Cloud Upload', variant: 'error' });
		}
	};

	// Submit the selected items as an rclone cloud upload scheduler job.
	const handleCloudUpload = async (e: React.FormEvent) => {
		e.preventDefault();
		if (selectedItems.length === 0 || !cloudRemoteId) return;
		const paths = selectedItems.map(name => currentPath === '/' ? `/${name}` : `${currentPath}/${name}`);
		setCloudUploading(true);
		try {
			const res = await fetch('/api/rclone/upload', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
				body: JSON.stringify({
					paths,
					remote_id: parseInt(cloudRemoteId, 10),
					dest_dir: cloudDestDir.trim(),
					keep_structure: cloudKeepStructure,
					require_public_link: cloudNeedLink,
				})
			});
			if (res.ok) {
				const data = await res.json();
				showGlobalAlert(`Cloud upload queued: ${data.upload_count} item(s), job #${data.job_id}. Track progress on the Cloud Storage page.`, { title: 'Upload Queued', variant: 'success' });
				setShowCloudUploadModal(false);
				setSelectedItems([]);
			} else {
				const errData = await res.json();
				showGlobalAlert(`Cloud upload failed: ${errData.error}`, { title: 'Upload Failed', variant: 'error' });
			}
		} catch (err: any) {
			showGlobalAlert(`Error uploading to cloud: ${err.message}`, { title: 'Error', variant: 'error' });
		} finally {
			setCloudUploading(false);
		}
	};

	// Bulk operations (one-by-one execution)
	const handleSendToTelegramBulk = async () => {
		if (selectedItems.length === 0) return;
		setLoading(true);
		try {
			for (const name of selectedItems) {
				const file = files.find(f => f.name === name);
				if (file && !file.is_dir) {
					const targetPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
					const res = await fetch('/api/telegram/send-file', {
						method: 'POST',
						headers: {
							'Content-Type': 'application/json',
							'Authorization': `Bearer ${token}`
						},
						body: JSON.stringify({ file_path: targetPath })
					});
					if (!res.ok) {
						const errData = await res.json();
						console.error(`Failed to send ${name}: ${errData.error}`);
					}
				}
			}
			showGlobalAlert(`Successfully queued ${selectedItems.length} send jobs to Telegram!`, { title: 'Bulk Upload Queued', variant: 'success' });
		} catch (err: any) {
			showGlobalAlert(`Error sending files to Telegram: ${err.message}`, { title: 'Error', variant: 'error' });
		} finally {
			setLoading(false);
		}
	};

	const handleDownloadBulk = () => {
		if (selectedItems.length === 0) return;
		selectedItems.forEach((name, index) => {
			const targetPath = currentPath === '/' ? `/${name}` : `${currentPath}/${name}`;
			const downloadUrl = `/api/files/stream?path=${encodeURIComponent(targetPath)}&download=true&token=${encodeURIComponent(token || '')}`;
			setTimeout(() => {
				const link = document.createElement('a');
				link.href = downloadUrl;
				link.download = name;
				document.body.appendChild(link);
				link.click();
				document.body.removeChild(link);
			}, index * 800);
		});
	};

	const handleRenameBulk = async () => {
		if (selectedItems.length === 0) return;
		setLoading(true);
		try {
			for (const name of selectedItems) {
				const file = files.find(f => f.name === name);
				if (!file) continue;
				const newName = await showGlobalPrompt(`Enter new name for "${file.name}":`, file.name, { title: 'Rename Item' });
				if (!newName || newName.trim() === '' || newName === file.name) continue;

				const src = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
				const dst = currentPath === '/' ? `/${newName.trim()}` : `${currentPath}/${newName.trim()}`;

				await fetch('/api/files/move', {
					method: 'POST',
					headers: {
						'Content-Type': 'application/json',
						'Authorization': `Bearer ${token}`
					},
					body: JSON.stringify({ src_path: src, dst_path: dst })
				});
			}
			fetchFiles(currentPath);
		} catch (err) {
			console.error("Bulk rename failed:", err);
		} finally {
			setLoading(false);
		}
	};

	const handleExtractBulk = async () => {
		const archiveFiles = selectedItems.filter(name => isArchiveFile(name));
		if (archiveFiles.length === 0) return;
		setLoading(true);
		try {
			for (const name of archiveFiles) {
				const targetPath = currentPath === '/' ? `/${name}` : `${currentPath}/${name}`;
				await fetch('/api/files/decompress', {
					method: 'POST',
					headers: {
						'Content-Type': 'application/json',
						'Authorization': `Bearer ${token}`
					},
					body: JSON.stringify({ path: targetPath })
				});
			}
			fetchFiles(currentPath);
			showGlobalAlert(`Extraction jobs queued for ${archiveFiles.length} archives!`, { title: 'Bulk Extraction Queued', variant: 'success' });
		} catch (err) {
			console.error("Bulk extract failed:", err);
		} finally {
			setLoading(false);
		}
	};

	const handleSearchResultClick = (result: FileItem & { path: string }) => {
		const idx = result.path.lastIndexOf('/');
		const parentPath = idx <= 0 ? '/' : result.path.substring(0, idx);

		setCurrentPath(parentPath);
		setShowSearchModal(false);

		setTimeout(() => {
			if (result.is_dir) {
				setCurrentPath(result.path);
			} else {
				openPreview(result);
				setSelectedItems([result.name]);
			}
		}, 150);
	};

	// Create Folder
	const handleCreateFolder = async (e: React.FormEvent) => {
		e.preventDefault();
		if (!newFolderName.trim()) return;
		setCreatingFolder(true);
		try {
			const res = await fetch('/api/files/create-folder', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({ parent_path: currentPath, folder_name: newFolderName.trim() })
			});
			if (res.ok) {
				setNewFolderName('');
				setShowFolderModal(false);
				fetchFiles(currentPath);
			}
		} catch (err) {
			console.error(err);
		} finally {
			setCreatingFolder(false);
		}
	};

	// Delete Selected
	const handleDeleteSelected = async () => {
		if (selectedItems.length === 0) return;
		if (!(await showGlobalConfirm(`Are you absolutely sure you want to permanently delete these ${selectedItems.length} items?`, { title: 'Delete Items', variant: 'warning' }))) return;
		
		setLoading(true);
		try {
			for (const name of selectedItems) {
				const targetPath = currentPath === '/' ? `/${name}` : `${currentPath}/${name}`;
				await fetch('/api/files/delete', {
					method: 'POST',
					headers: {
						'Content-Type': 'application/json',
						'Authorization': `Bearer ${token}`
					},
					body: JSON.stringify({ path: targetPath })
				});
			}
			fetchFiles(currentPath);
		} catch (err) {
			console.error(err);
		} finally {
			setLoading(false);
		}
	};

	// Single Delete
	const handleDelete = async (file: FileItem) => {
		if (!(await showGlobalConfirm(`Are you sure you want to permanently delete "${file.name}"?`, { title: 'Delete Item', variant: 'warning' }))) return;
		const targetPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
		setLoading(true);
		try {
			const res = await fetch('/api/files/delete', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({ path: targetPath })
			});
			if (res.ok) {
				fetchFiles(currentPath);
				if (previewFile?.name === file.name) {
					closePreview();
				}
			}
		} catch (err) {
			console.error(err);
		} finally {
			setLoading(false);
		}
	};

	const handleUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
		const filesUploaded = e.target.files;
		if (!filesUploaded || filesUploaded.length === 0) return;
		setUploading(true);
		try {
			const formData = new FormData();
			formData.append('path', currentPath);
			formData.append('file', filesUploaded[0]);

			const res = await fetch('/api/files/upload', {
				method: 'POST',
				headers: { 'Authorization': `Bearer ${token}` },
				body: formData
			});
			if (res.ok) {
				fetchFiles(currentPath);
			}
		} catch (err) {
			console.error(err);
		} finally {
			setUploading(false);
			if (fileInputRef.current) fileInputRef.current.value = '';
		}
	};

	// Open Previewer
	const openPreview = async (file: FileItem) => {
		setPreviewFile(file);
		const cat = getFileCategory(file.extension);
		const targetPath = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;

		if (cat === 'image') {
			setPreviewType('image');
			setImgZoom(1);
			setImgRotation(0);
		} else if (cat === 'video') {
			setPreviewType('video');
			setDetectedMetadata(null);
			const videoUrl = `/api/files/stream?path=${encodeURIComponent(targetPath)}&token=${encodeURIComponent(token || '')}`;
			const tempVideo = document.createElement('video');
			tempVideo.src = videoUrl;
			tempVideo.muted = true;
			tempVideo.preload = 'metadata';
			tempVideo.onloadedmetadata = () => {
				setDetectedMetadata({
					width: tempVideo.videoWidth,
					height: tempVideo.videoHeight,
					duration: tempVideo.duration
				});
			};
		} else if (cat === 'code') {
			setPreviewType('editor');
			setEditorLang(getMonacoLanguage(file.extension));
			setEditorLoading(true);
			try {
				const res = await fetch(`/api/files/content?path=${encodeURIComponent(targetPath)}`, {
					headers: { 'Authorization': `Bearer ${token}` }
				});
				if (res.ok) {
					const data = await res.json();
					setEditorContent(data.content || '');
				}
			} catch (err) {
				console.error("Failed to read text file content", err);
			} finally {
				setEditorLoading(false);
			}
		} else {
			setPreviewType('other');
		}
	};

	const closePreview = () => {
		setPreviewFile(null);
		setPreviewType(null);
		setEditorContent('');
		setDetectedMetadata(null);
	};

	const handleItemClick = (file: FileItem) => {
		if (file.is_dir) {
			navigateTo(file.name);
		} else {
			openPreview(file);
		}
	};

	const handleItemDoubleClick = (file: FileItem) => {
		if (!file.is_dir && getFileCategory(file.extension) === 'video') {
			setVideoModalFile(file);
			setShowVideoModal(true);
		}
	};

	const handleSaveContent = async () => {
		if (!previewFile) return;
		setSavingContent(true);
		const targetPath = currentPath === '/' ? `/${previewFile.name}` : `${currentPath}/${previewFile.name}`;
		try {
			const res = await fetch('/api/files/save', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({ path: targetPath, content: editorContent })
			});
			if (res.ok) {
				showGlobalAlert('File saved successfully!', { title: 'Saved', variant: 'success' });
			}
		} catch (err) {
			console.error(err);
		} finally {
			setSavingContent(false);
		}
	};

	// Stats calculations
	const totalFolders = files.filter(f => f.is_dir).length;
	const totalFiles = files.filter(f => !f.is_dir).length;
	const totalSize = files.reduce((acc, f) => acc + (f.is_dir ? 0 : f.size), 0);

	const imageSize = files.reduce((acc, f) => getFileCategory(f.extension) === 'image' && !f.is_dir ? acc + f.size : acc, 0);
	const videoSize = files.reduce((acc, f) => getFileCategory(f.extension) === 'video' && !f.is_dir ? acc + f.size : acc, 0);
	const codeSize = files.reduce((acc, f) => getFileCategory(f.extension) === 'code' && !f.is_dir ? acc + f.size : acc, 0);
	const otherSize = files.reduce((acc, f) => getFileCategory(f.extension) === 'other' && !f.is_dir ? acc + f.size : acc, 0);

	// Split current path for dynamic breadcrumbs
	const pathParts = currentPath.split('/').filter(Boolean);

	return (
		<div style={{ display: 'flex', flexDirection: 'column', gap: 14, height: 'calc(100vh - 120px)' }}>
			{/* Top Action & Path Breadcrumbs Toolbar */}
			<div className="g-card" style={{ padding: '12px 18px', display: 'flex', flexWrap: 'wrap', gap: 12, alignItems: 'center', justifyContent: 'space-between' }}>
				<div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
					<button className="btn btn--sm" onClick={navigateUp} disabled={currentPath === '/'} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
						<FiArrowLeft size={14} /> Back
					</button>

					{/* Responsive Breadcrumbs */}
					<div style={{ display: 'flex', alignItems: 'center', gap: 4, fontFamily: 'monospace', fontSize: 13, background: 'var(--color-brand-bg)', padding: '6px 12px', borderRadius: 6, border: '1px solid var(--color-brand-border)' }}>
						<span style={{ cursor: 'pointer', color: 'var(--color-brand)' }} onClick={() => setCurrentPath('/')}>root</span>
						{pathParts.map((part, index) => (
							<React.Fragment key={index}>
								<span style={{ color: 'var(--color-brand-muted)' }}>/</span>
								<span 
									style={{ 
										cursor: index === pathParts.length - 1 ? 'default' : 'pointer', 
										color: index === pathParts.length - 1 ? 'var(--color-brand-heading)' : 'var(--color-brand)',
										fontWeight: index === pathParts.length - 1 ? 600 : 400
									}} 
									onClick={() => index !== pathParts.length - 1 && navigateToBreadcrumb(index, pathParts)}
								>
									{part}
								</span>
							</React.Fragment>
						))}
					</div>
				</div>

				{/* Operations toolbar */}
				<div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, alignItems: 'center' }}>
					{/* Paste Button active when items in clipboard */}
					{clipboard.action && (
						<button 
							className="btn btn--primary btn--sm animate-pulse" 
							onClick={handlePaste}
							style={{ display: 'flex', alignItems: 'center', gap: 6, background: '#10b981', borderColor: '#10b981' }}
						>
							<FiClipboard size={14} /> Paste ({clipboard.items.length})
						</button>
					)}

					{/* Search */}
					<div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
						<input 
							type="text" 
							placeholder="Search current folder..."
							value={searchQuery}
							onChange={(e) => setSearchQuery(e.target.value)}
							style={{
								padding: '6px 10px',
								fontSize: 12,
								borderRadius: 6,
								border: '1px solid var(--color-brand-border)',
								background: 'var(--color-brand-bg)',
								color: 'var(--color-brand-heading)',
								width: 150
							}}
						/>
						<button 
							type="button"
							className="btn btn--sm" 
							onClick={() => setShowSearchModal(true)} 
							style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '6px 8px', color: 'var(--color-brand)', background: 'var(--color-brand-light)', border: '1px solid var(--color-brand)', height: '28px' }}
							title="Global Physical Disk Search"
						>
							<FiSearch size={14} />
						</button>
					</div>

					{/* Sorting Dropdowns */}
					<div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
						<select 
							value={sortBy} 
							onChange={(e) => setSortBy(e.target.value as any)}
							style={{
								padding: '5px 8px',
								fontSize: 12,
								borderRadius: 6,
								background: 'var(--color-brand-bg)',
								border: '1px solid var(--color-brand-border)',
								color: 'var(--color-brand-heading)'
							}}
						>
							<option value="name">Name</option>
							<option value="size">Size</option>
							<option value="time">Mod Time</option>
							<option value="type">Type</option>
						</select>
						<button 
							onClick={() => setSortOrder(prev => prev === 'asc' ? 'desc' : 'asc')}
							className="btn btn--sm" 
							style={{ padding: '6px 8px' }}
						>
							{sortOrder === 'asc' ? '↑' : '↓'}
						</button>
					</div>

					{/* Grid/List Toggle */}
					<div style={{ display: 'flex', border: '1px solid var(--color-brand-border)', borderRadius: 6, overflow: 'hidden' }}>
						<button 
							onClick={() => setViewMode('grid')}
							style={{ padding: '6px 8px', background: viewMode === 'grid' ? 'var(--color-brand-border)' : 'var(--color-brand-bg)', border: 'none', cursor: 'pointer', color: 'var(--color-brand-heading)', display: 'flex', alignItems: 'center' }}
						>
							<FiGrid size={14} />
						</button>
						<button 
							onClick={() => setViewMode('list')}
							style={{ padding: '6px 8px', background: viewMode === 'list' ? 'var(--color-brand-border)' : 'var(--color-brand-bg)', border: 'none', cursor: 'pointer', color: 'var(--color-brand-heading)', display: 'flex', alignItems: 'center' }}
						>
							<FiList size={14} />
						</button>
					</div>

					{/* Compress List Trigger Button */}
					{(compressQueue.length > 0 || selectedItems.length > 0) && (
						<button 
							className="btn btn--sm" 
							onClick={() => setShowCompressQueueModal(true)} 
							style={{ 
								display: 'flex', 
								alignItems: 'center', 
								gap: 6, 
								background: 'rgba(99, 102, 241, 0.1)', 
								border: '1px solid rgba(99, 102, 241, 0.3)', 
								color: 'var(--color-brand)',
								fontWeight: 600
							}}
							title="Open Compress List Queue"
						>
							<FiArchive size={14} />
							<span>Compress List ({compressQueue.length})</span>
						</button>
					)}

					<button className="btn btn--sm" onClick={() => fetchFiles(currentPath)} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
						<FiRefreshCw size={13} className={loading ? 'spin-anim' : ''} />
					</button>

					<button className="btn btn--sm" onClick={() => setShowFolderModal(true)} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
						<FiPlus size={14} /> New Folder
					</button>

					<input 
						type="file" 
						ref={fileInputRef} 
						onChange={handleUpload} 
						style={{ display: 'none' }} 
					/>
					<button className="btn btn--primary btn--sm" onClick={() => fileInputRef.current?.click()} disabled={uploading} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
						<FiUpload size={14} /> Upload
					</button>
				</div>
			</div>

			{/* Advanced Storage State Card */}
			<div className="g-card animate-slide-in" style={{ padding: '14px 20px', display: 'flex', flexDirection: 'column', gap: 10 }}>
				<div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', alignItems: 'center', gap: 10 }}>
					<div style={{ display: 'flex', gap: 20 }}>
						<div style={{ display: 'flex', flexDirection: 'column' }}>
							<span style={{ fontSize: 10, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Total Folders</span>
							<span style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-brand-heading)' }}>{totalFolders}</span>
						</div>
						<div style={{ display: 'flex', flexDirection: 'column' }}>
							<span style={{ fontSize: 10, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Total Files</span>
							<span style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-brand-heading)' }}>{totalFiles}</span>
						</div>
						<div style={{ display: 'flex', flexDirection: 'column' }}>
							<span style={{ fontSize: 10, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Storage Occupied</span>
							<span style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-brand-heading)' }}>{formatSize(totalSize)}</span>
						</div>
						{diskTotal > 0 && (
							<div style={{ display: 'flex', flexDirection: 'column', borderLeft: '1px solid var(--color-brand-border)', paddingLeft: 20 }}>
								<span style={{ fontSize: 10, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Server Machine HDD</span>
								<span style={{ fontSize: 13, fontWeight: 600, color: 'var(--color-brand-heading)', marginTop: 2 }}>
									{formatSize(diskUsed)} / {formatSize(diskTotal)} ({((diskUsed / diskTotal) * 100).toFixed(0)}% Used, {formatSize(diskFree)} Free)
								</span>
							</div>
						)}
						{s3Enabled && (
							<div style={{ display: 'flex', flexDirection: 'column', borderLeft: '1px solid var(--color-brand-border)', paddingLeft: 20 }}>
								<span style={{ fontSize: 10, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>S3 Object Storage</span>
								<span style={{ fontSize: 13, fontWeight: 600, color: 'var(--color-brand-heading)', marginTop: 2, display: 'flex', alignItems: 'center', gap: 4 }}>
									<FiCloud size={12} style={{ color: '#3b82f6' }} />
									{s3ObjectCount} objects · {formatSize(s3TotalSize)}
								</span>
							</div>
						)}
					</div>
					
					{/* Status labels */}
					<div style={{ display: 'flex', gap: 14, fontSize: 11 }}>
						<span style={{ display: 'flex', alignItems: 'center', gap: 4, color: 'var(--color-brand-text)' }}>
							<span style={{ width: 8, height: 8, borderRadius: '50%', background: '#22c55e' }} /> Images ({formatSize(imageSize)})
						</span>
						<span style={{ display: 'flex', alignItems: 'center', gap: 4, color: 'var(--color-brand-text)' }}>
							<span style={{ width: 8, height: 8, borderRadius: '50%', background: '#a855f7' }} /> Videos ({formatSize(videoSize)})
						</span>
						<span style={{ display: 'flex', alignItems: 'center', gap: 4, color: 'var(--color-brand-text)' }}>
							<span style={{ width: 8, height: 8, borderRadius: '50%', background: '#3b82f6' }} /> Code ({formatSize(codeSize)})
						</span>
						<span style={{ display: 'flex', alignItems: 'center', gap: 4, color: 'var(--color-brand-text)' }}>
							<span style={{ width: 8, height: 8, borderRadius: '50%', background: '#64748b' }} /> Others ({formatSize(otherSize)})
						</span>
					</div>
				</div>

				{/* Visual Distribution Bar */}
				<div style={{ height: 8, width: '100%', background: 'var(--color-brand-border)', borderRadius: 4, overflow: 'hidden', display: 'flex' }}>
					{totalSize > 0 ? (
						<>
							<div style={{ width: `${(imageSize / totalSize) * 100}%`, background: '#22c55e', transition: 'width 0.3s' }} title={`Images: ${((imageSize / totalSize) * 100).toFixed(1)}%`} />
							<div style={{ width: `${(videoSize / totalSize) * 100}%`, background: '#a855f7', transition: 'width 0.3s' }} title={`Videos: ${((videoSize / totalSize) * 100).toFixed(1)}%`} />
							<div style={{ width: `${(codeSize / totalSize) * 100}%`, background: '#3b82f6', transition: 'width 0.3s' }} title={`Code/Text: ${((codeSize / totalSize) * 100).toFixed(1)}%`} />
							<div style={{ width: `${(otherSize / totalSize) * 100}%`, background: '#64748b', transition: 'width 0.3s' }} title={`Others: ${((otherSize / totalSize) * 100).toFixed(1)}%`} />
						</>
					) : (
						<div style={{ width: '100%', background: 'rgba(99,102,241,0.08)' }} />
					)}
				</div>
			</div>



			{/* Main Workspace Pane split into Sidebar Directory Tree and File Cards Grid */}
			<div style={{ flex: 1, display: 'flex', gap: 14, overflow: 'hidden' }}>
				{/* Left Sidebar Interactive Directory tree */}
				<div 
					className="g-card" 
					style={{ 
						width: 220, 
						padding: '14px 16px', 
						display: 'flex', 
						flexDirection: 'column', 
						overflowY: 'auto',
						flexShrink: 0,
						borderRight: '1px solid var(--color-brand-border)'
					}}
				>
					<h4 style={{ fontSize: 11, fontWeight: 700, textTransform: 'uppercase', color: 'var(--color-brand-muted)', letterSpacing: '0.05em', marginBottom: 12 }}>
						Folders Tree
					</h4>
					<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
						{folderTree.map(path => {
							const display = path === '/' ? 'root' : path.split('/').pop();
							const depth = path.split('/').filter(Boolean).length;
							const isActive = currentPath === path;
							return (
								<div 
									key={path}
									onClick={() => setCurrentPath(path)}
									style={{
										display: 'flex',
										alignItems: 'center',
										gap: 6,
										padding: '6px 8px',
										borderRadius: 6,
										fontSize: 12,
										cursor: 'pointer',
										marginLeft: depth * 10,
										background: isActive ? 'rgba(99,102,241,0.06)' : 'transparent',
										border: isActive ? '1px solid var(--color-brand)' : '1px solid transparent',
										color: isActive ? 'var(--color-brand-heading)' : 'var(--color-brand-text)',
										fontWeight: isActive ? 600 : 400,
										transition: 'all 0.12s'
									}}
								>
									<FiFolder size={14} style={{ color: isActive ? 'var(--color-brand)' : '#eab308' }} />
									<span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{display}</span>
								</div>
							);
						})}
					</div>
				</div>

				{/* Middle Panel Workspace containing main directory cards */}
				<div 
					className="g-card" 
					style={{ 
						flex: 1, 
						display: 'flex',
						flexDirection: 'column',
						padding: 0, 
						overflow: 'hidden',
						boxShadow: 'inset 0 2px 8px rgba(0,0,0,0.04)'
					}}
				>
					{/* Actions Header of the File List */}
					<div style={{
						display: 'flex',
						alignItems: 'center',
						justifyContent: 'space-between',
						padding: '10px 14px',
						borderBottom: '1px solid var(--color-brand-border)',
						background: 'var(--color-brand-card)',
						minHeight: '48px',
						flexShrink: 0
					}}>
						{/* Left section: Title or selected info */}
						<div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
							<span style={{ fontSize: 13, fontWeight: 700, color: 'var(--color-brand-heading)' }}>
								{selectedItems.length > 0 ? `${selectedItems.length} items selected` : 'File List Actions'}
							</span>
						</div>
						
						{/* Right section: Action Icons */}
						<div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
							{/* Preview Button */}
							<button 
								type="button"
								disabled={selectedItems.length === 0}
								onClick={() => {
									const firstFile = files.find(f => f.name === selectedItems[0]);
									if (firstFile) openPreview(firstFile);
								}}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'var(--color-brand-light)' : 'transparent',
									color: selectedItems.length > 0 ? 'var(--color-brand)' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Preview first selected file"
							>
								<FiEye size={14} /> Preview
							</button>

							{/* Send to Telegram Button */}
							<button 
								type="button"
								disabled={selectedItems.length === 0}
								onClick={handleSendToTelegramBulk}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'var(--color-brand-light)' : 'transparent',
									color: selectedItems.length > 0 ? 'var(--color-brand-green, #22c55e)' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Send selected files to Telegram one by one"
							>
							<FiSend size={14} /> Send Telegram
						</button>

						{/* Move to S3 Button (cut: upload + delete local) */}
						{s3Enabled && (
							<button
								type="button"
								disabled={selectedItems.length === 0}
								onClick={handleMoveToS3Bulk}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'rgba(59,130,246,0.1)' : 'transparent',
									color: selectedItems.length > 0 ? '#3b82f6' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Move selected items to S3 storage (cut: upload + delete local)"
							>
								<FiCloud size={14} /> Move to S3
							</button>
						)}

						{/* Upload to Cloud Button (rclone universal storage) */}
						<button
							type="button"
							disabled={selectedItems.length === 0}
							onClick={openCloudUploadModal}
							style={{
								padding: '6px 10px',
								borderRadius: 6,
								border: '1px solid var(--color-brand-border)',
								background: selectedItems.length > 0 ? 'rgba(99,102,241,0.1)' : 'transparent',
								color: selectedItems.length > 0 ? '#6366f1' : 'var(--color-brand-muted)',
								cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
								opacity: selectedItems.length > 0 ? 1 : 0.5,
								display: 'flex',
								alignItems: 'center',
								gap: 4,
								fontSize: 12,
								fontWeight: 600,
								transition: 'all 0.15s'
							}}
							title="Upload selected items to a configured cloud remote"
						>
							<FiUploadCloud size={14} /> Upload to Cloud
						</button>

						{/* Share Link / Download Button */}
							<button 
								type="button"
								disabled={selectedItems.length === 0}
								onClick={handleDownloadBulk}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'var(--color-brand-light)' : 'transparent',
									color: selectedItems.length > 0 ? 'var(--color-brand-blue, #3b82f6)' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Download selected files one by one"
							>
								<FiDownload size={14} /> Download
							</button>

							{/* Rename Button */}
							<button 
								type="button"
								disabled={selectedItems.length === 0}
								onClick={handleRenameBulk}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'var(--color-brand-light)' : 'transparent',
									color: selectedItems.length > 0 ? 'var(--color-brand-indigo, #6366f1)' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Rename selected files one by one"
							>
								<FiEdit3 size={14} /> Rename
							</button>

							{/* Copy Button */}
							<button 
								type="button"
								disabled={selectedItems.length === 0}
								onClick={handleCopy}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'var(--color-brand-light)' : 'transparent',
									color: selectedItems.length > 0 ? 'var(--color-brand-heading)' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Copy selected items to clipboard"
							>
								<FiCopy size={14} /> Copy
							</button>

							{/* Cut Button */}
							<button 
								type="button"
								disabled={selectedItems.length === 0}
								onClick={handleCut}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'var(--color-brand-light)' : 'transparent',
									color: selectedItems.length > 0 ? 'var(--color-brand-heading)' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Cut selected items to clipboard"
							>
								<FiScissors size={14} /> Cut
							</button>

							{/* Add to Compress List Button */}
							<button 
								type="button"
								disabled={selectedItems.length === 0}
								onClick={handleAddSelectedToCompressQueue}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'var(--color-brand-light)' : 'transparent',
									color: selectedItems.length > 0 ? 'var(--color-brand-indigo, #6366f1)' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Add selected items to the compress queue"
							>
								<FiArchive size={14} /> Add to Compress List
							</button>

							{/* Uncompress Button */}
							{selectedItems.some(name => isArchiveFile(name)) && (
								<button 
									type="button"
									onClick={handleExtractBulk}
									style={{
										padding: '6px 10px',
										borderRadius: 6,
										border: '1px solid var(--color-brand-border)',
										background: 'rgba(59,130,246,0.1)',
										color: '#3b82f6',
										cursor: 'pointer',
										display: 'flex',
										alignItems: 'center',
										gap: 4,
										fontSize: 12,
										fontWeight: 600,
										transition: 'all 0.15s'
									}}
									title="Uncompress selected archives near their original files"
								>
									<FiArchive size={14} /> Uncompress
								</button>
							)}

							{/* Delete Button */}
							<button 
								type="button"
								disabled={selectedItems.length === 0}
								onClick={handleDeleteSelected}
								style={{
									padding: '6px 10px',
									borderRadius: 6,
									border: '1px solid var(--color-brand-border)',
									background: selectedItems.length > 0 ? 'rgba(239,68,68,0.1)' : 'transparent',
									color: selectedItems.length > 0 ? 'var(--color-brand-red)' : 'var(--color-brand-muted)',
									cursor: selectedItems.length > 0 ? 'pointer' : 'not-allowed',
									opacity: selectedItems.length > 0 ? 1 : 0.5,
									display: 'flex',
									alignItems: 'center',
									gap: 4,
									fontSize: 12,
									fontWeight: 600,
									transition: 'all 0.15s'
								}}
								title="Delete selected files one by one"
							>
								<FiTrash2 size={14} /> Delete
							</button>
						</div>
					</div>

					{/* File grid/list scroll wrapper */}
					<div style={{ flex: 1, padding: '16px 18px', overflowY: 'auto', display: 'flex', flexDirection: 'column' }}>
						{loading ? (
						<div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%' }}>
							<div style={{ width: 28, height: 28, border: '3px solid var(--color-brand-border)', borderTopColor: 'var(--color-brand)', borderRadius: '50%', animation: 'spin .6s linear infinite' }} />
						</div>
					) : filteredFiles.length === 0 ? (
						<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', height: '100%', gap: 12, color: 'var(--color-brand-muted)' }}>
							<FiFolder size={44} />
							<span style={{ fontSize: 13 }}>Folder is empty</span>
						</div>
					) : viewMode === 'grid' ? (
						<div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(135px, 1fr))', gap: 12 }}>
							{filteredFiles.map((file) => {
								const isSelected = selectedItems.includes(file.name);
								return (
									<div 
										key={file.name}
										style={{
											border: isSelected ? '2px solid var(--color-brand)' : '1px solid var(--color-brand-border)',
											borderRadius: 12,
											padding: 12,
											background: isSelected ? 'rgba(99,102,241,0.03)' : 'var(--color-brand-card)',
											display: 'flex',
											flexDirection: 'column',
											alignItems: 'center',
											justifyContent: 'space-between',
											cursor: 'pointer',
											position: 'relative',
											height: 135,
											transition: 'all 0.12s'
										}}
										onClick={() => handleItemClick(file)}
										onDoubleClick={() => handleItemDoubleClick(file)}
										onContextMenu={(e) => handleContextMenu(e, file)}
										className="file-card-hover"
									>
										{/* Selection Checkbox */}
										<input 
											type="checkbox"
											checked={isSelected}
											onChange={(e) => { e.stopPropagation(); toggleSelectItem(file.name); }}
											style={{ position: 'absolute', left: 8, top: 8, cursor: 'pointer', width: 14, height: 14 }}
										/>

										{/* Visual Icon */}
										<div style={{ marginTop: 10, position: 'relative' }}>
											{getFileIcon(file)}
											{file.s3_key && (
												<span style={{ position: 'absolute', top: -2, right: -8, display: 'flex', alignItems: 'center', justifyContent: 'center', width: 16, height: 16, borderRadius: '50%', background: '#3b82f6', color: '#fff', fontSize: 8, border: '1px solid var(--color-brand-card)' }} title="Stored in S3 Object Storage">
													<FiCloud size={8} />
												</span>
											)}
										</div>

										{/* Name and size details */}
										<div style={{ width: '100%', textAlign: 'center', marginTop: 8 }}>
											<div 
												style={{ 
													fontSize: 12, 
													fontWeight: 600, 
													color: 'var(--color-brand-heading)', 
													overflow: 'hidden', 
													textOverflow: 'ellipsis', 
													whiteSpace: 'nowrap' 
												}}
												title={file.name}
											>
												{file.name}
											</div>
											<div style={{ fontSize: 9, color: 'var(--color-brand-text)', marginTop: 2 }}>
												{file.is_dir ? 'Folder' : formatSize(file.size)}
											</div>
										</div>
									</div>
								);
							})}
						</div>
					) : (
						/* List Mode */
						<div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
							<div style={{ display: 'flex', alignItems: 'center', padding: '6px 14px', borderBottom: '1px solid var(--color-brand-border)', fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>
								<input type="checkbox" checked={selectedItems.length === filteredFiles.length && filteredFiles.length > 0} onChange={toggleSelectAll} style={{ marginRight: 12, cursor: 'pointer' }} />
								<span style={{ flex: 1 }}>Name</span>
								<span style={{ width: 80, textAlign: 'right' }}>Size</span>
								<span style={{ width: 150, paddingLeft: 20 }}>Mod Date</span>
							</div>

							{filteredFiles.map((file) => {
								const isSelected = selectedItems.includes(file.name);
								return (
									<div 
										key={file.name}
										style={{
											display: 'flex',
											alignItems: 'center',
											padding: '8px 14px',
											borderRadius: 8,
											background: isSelected ? 'rgba(99,102,241,0.04)' : 'var(--color-brand-card)',
											border: isSelected ? '1px solid var(--color-brand)' : '1px solid var(--color-brand-border)',
											cursor: 'pointer',
											transition: 'all 0.12s'
										}}
										onClick={() => handleItemClick(file)}
										onDoubleClick={() => handleItemDoubleClick(file)}
										onContextMenu={(e) => handleContextMenu(e, file)}
										className="file-row-hover"
									>
										<input 
											type="checkbox"
											checked={isSelected}
											onChange={(e) => { e.stopPropagation(); toggleSelectItem(file.name); }}
											style={{ marginRight: 12, cursor: 'pointer', width: 14, height: 14 }}
										/>

										<div style={{ display: 'flex', alignItems: 'center', gap: 10, flex: 1, minWidth: 0 }}>
											<div style={{ display: 'flex', flexShrink: 0 }}>
												{file.is_dir ? <FiFolder style={{ color: '#eab308', fontSize: 20 }} /> : <FiFile style={{ color: '#64748b', fontSize: 20 }} />}
											</div>
											<span style={{ fontSize: 13, fontWeight: 600, color: 'var(--color-brand-heading)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
												{file.name}
											</span>
											{file.s3_key && (
												<span style={{ display: 'flex', alignItems: 'center', gap: 2, fontSize: 9, color: '#3b82f6', fontWeight: 600, background: 'rgba(59,130,246,0.1)', padding: '1px 5px', borderRadius: 4, whiteSpace: 'nowrap', flexShrink: 0 }} title="Stored in S3 Object Storage">
													<FiCloud size={9} /> S3
												</span>
											)}
										</div>

										<div style={{ display: 'flex', alignItems: 'center', gap: 24, fontSize: 11, color: 'var(--color-brand-text)' }}>
											<span style={{ width: 80, textAlign: 'right' }}>{file.is_dir ? 'DIR' : formatSize(file.size)}</span>
											<span style={{ width: 140 }}>{new Date(file.mod_time).toLocaleString()}</span>
										</div>
									</div>
								);
							})}
						</div>
					)}
					</div>
				</div>

				{/* Right Info Details & Previews Panel */}
				{previewFile && (
					<div 
						className="g-card animate-slide-in" 
						style={{ 
							width: 420, 
							padding: '16px 18px', 
							display: 'flex', 
							flexDirection: 'column', 
							borderLeft: '1px solid var(--color-brand-border)',
							flexShrink: 0
						}}
					>
						{/* Preview Header */}
						<div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', borderBottom: '1px solid var(--color-brand-border)', paddingBottom: 10, marginBottom: 12 }}>
							<div>
								<h3 style={{ fontSize: 13, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0, overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: 260, whiteSpace: 'nowrap', display: 'flex', alignItems: 'center', gap: 5 }}>
									{previewFile.name}
									{previewFile.s3_key && (
										<span style={{ display: 'inline-flex', alignItems: 'center', gap: 2, color: '#3b82f6', fontWeight: 600, fontSize: 10, background: 'rgba(59,130,246,0.1)', padding: '1px 5px', borderRadius: 4, flexShrink: 0 }} title="Stored in S3 Object Storage">
											<FiCloud size={10} /> S3
										</span>
									)}
								</h3>
								<span style={{ fontSize: 10, color: 'var(--color-brand-text)' }}>
									{previewFile.is_dir ? 'Directory Node' : formatSize(previewFile.size)}
								</span>
							</div>
							<div style={{ display: 'flex', gap: 4 }}>
								{/* Direct link generator */}
								{!previewFile.is_dir && (
									<button 
										className="btn btn--sm" 
										onClick={() => handleShareLink(previewFile)}
										style={{ display: 'flex', padding: 6 }} 
										title="Share File Direct URL"
									>
										<FiShare2 size={13} />
									</button>
								)}
								<button 
									onClick={closePreview} 
									style={{ border: 'none', background: 'var(--color-brand-bg)', borderRadius: '50%', cursor: 'pointer', padding: 6, display: 'flex', color: 'var(--color-brand-heading)' }}
								>
									<FiX size={13} />
								</button>
							</div>
						</div>

						{/* Inline File Action operations */}
						<div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 12 }}>
							<button className="btn btn--sm" onClick={() => handleRename(previewFile)} style={{ display: 'flex', alignItems: 'center', gap: 4 }}><FiEdit3 size={12} /> Rename</button>
							{previewFile.name.endsWith('.zip') && (
								<button className="btn btn--sm" onClick={() => handleExtract(previewFile)} style={{ display: 'flex', alignItems: 'center', gap: 4, background: 'rgba(59,130,246,0.1)', color: '#3b82f6', borderColor: '#3b82f6' }}><FiArchive size={12} /> Extract ZIP</button>
							)}
							<button className="btn btn--sm" onClick={() => handleDelete(previewFile)} style={{ display: 'flex', alignItems: 'center', gap: 4, color: '#ef4444', borderColor: '#ef4444' }}><FiTrash2 size={12} /> Delete</button>
						</div>

						{/* Preview Workspace Body */}
						<div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
							{/* Image Viewer */}
							{previewType === 'image' && (
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 10, overflow: 'hidden' }}>
									<div 
										style={{ 
											flex: 1, 
											background: 'var(--color-brand-bg)', 
											borderRadius: 8, 
											border: '1px solid var(--color-brand-border)',
											display: 'flex', 
											alignItems: 'center', 
											justifyContent: 'center',
											overflow: 'hidden',
											position: 'relative'
										}}
									>
										{previewFile && (
											<button 
												className="btn btn--sm btn--primary" 
												onClick={() => {
													const idx = imageFiles.findIndex(f => f.name === previewFile.name);
													if (idx !== -1) {
														setLightboxIndex(idx);
														setIsLightboxOpen(true);
													}
												}}
												style={{
													position: 'absolute',
													top: 10,
													right: 10,
													zIndex: 5,
													display: 'flex',
													alignItems: 'center',
													gap: 6
												}}
											>
												<FiMaximize2 size={13} /> Full Screen Gallery
											</button>
										)}
										<img 
											src={`/api/files/stream?path=${encodeURIComponent(currentPath === '/' ? `/${previewFile.name}` : `${currentPath}/${previewFile.name}`)}&token=${encodeURIComponent(token || '')}`} 
											alt={previewFile.name}
											style={{
												maxWidth: '95%',
												maxHeight: '95%',
												transform: `scale(${imgZoom}) rotate(${imgRotation}deg)`,
												transition: 'transform 0.12s ease-in-out',
												borderRadius: 4,
												objectFit: 'contain'
											}}
										/>
									</div>
									<div style={{ display: 'flex', justifyContent: 'center', gap: 8 }}>
										<button className="btn btn--sm" onClick={() => setImgZoom(prev => Math.min(prev + 0.2, 3))}><FiZoomIn size={13} /></button>
										<button className="btn btn--sm" onClick={() => setImgZoom(prev => Math.max(prev - 0.2, 0.4))}><FiZoomOut size={13} /></button>
										<button className="btn btn--sm" onClick={() => setImgRotation(prev => prev + 90)}><FiRotateCw size={13} /></button>
									</div>
								</div>
							)}

							{/* Video Details Card (No Player in right box) */}
							{previewType === 'video' && previewFile && (
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 14 }}>
									<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', padding: '16px 0', background: 'rgba(168,85,247,0.04)', borderRadius: 12, border: '1px dashed rgba(168,85,247,0.2)' }}>
										<FiVideo size={48} style={{ color: '#a855f7', marginBottom: 10 }} />
										<button 
											className="btn btn--primary" 
											onClick={() => {
												setVideoModalFile(previewFile);
												setShowVideoModal(true);
											}}
											style={{ display: 'flex', alignItems: 'center', gap: 8, background: '#a855f7', borderColor: '#a855f7' }}
										>
											<FiPlay size={14} fill="currentColor" /> Play in Cinema Mode
										</button>
										<span style={{ fontSize: 10, color: 'var(--color-brand-muted)', marginTop: 6 }}>
											Or double-click the file to play
										</span>
									</div>

									<div style={{ background: 'var(--color-brand-bg)', borderRadius: 8, padding: 14, border: '1px solid var(--color-brand-border)' }}>
										<h4 style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', marginBottom: 8, letterSpacing: '0.05em' }}>Video Specifications</h4>
										<table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
											<tbody>
												<tr style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
													<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>Resolution</td>
													<td style={{ padding: '6px 0', textAlign: 'right', color: 'var(--color-brand-heading)', fontWeight: 600 }}>
														{detectedMetadata ? `${detectedMetadata.width} x ${detectedMetadata.height} (${getResolutionLabel(detectedMetadata.width, detectedMetadata.height)})` : 'Loading...'}
													</td>
												</tr>
												<tr style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
													<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>Duration</td>
													<td style={{ padding: '6px 0', textAlign: 'right', color: 'var(--color-brand-heading)' }}>
														{detectedMetadata ? formatDuration(detectedMetadata.duration) : 'Loading...'}
													</td>
												</tr>
												<tr style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
													<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>File Format</td>
													<td style={{ padding: '6px 0', textAlign: 'right', color: 'var(--color-brand-heading)' }}>{previewFile.extension.toUpperCase()}</td>
												</tr>
												<tr style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
													<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>File Size</td>
													<td style={{ padding: '6px 0', textAlign: 'right', color: 'var(--color-brand-heading)' }}>{formatSize(previewFile.size)}</td>
												</tr>
												<tr>
													<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>Modified</td>
													<td style={{ padding: '6px 0', textAlign: 'right', color: 'var(--color-brand-heading)' }}>{new Date(previewFile.mod_time).toLocaleString()}</td>
												</tr>
												{previewFile.s3_key && (
													<tr>
														<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>Storage</td>
														<td style={{ padding: '6px 0', textAlign: 'right' }}>
															<span style={{ display: 'inline-flex', alignItems: 'center', gap: 3, color: '#3b82f6', fontWeight: 600, fontSize: 11 }}>
																<FiCloud size={11} /> S3 Object
															</span>
														</td>
													</tr>
												)}
											</tbody>
										</table>
									</div>

									<div style={{ display: 'flex', gap: 8 }}>
										<a 
											href={`/api/files/stream?path=${encodeURIComponent(currentPath === '/' ? `/${previewFile.name}` : `${currentPath}/${previewFile.name}`)}&download=true&token=${encodeURIComponent(token || '')}`} 
											download
											className="btn btn--primary"
											style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }}
										>
											<FiDownload size={14} /> Download File
										</a>
										<button 
											className="btn" 
											onClick={() => {
												const targetPath = currentPath === '/' ? `/${previewFile.name}` : `${currentPath}/${previewFile.name}`;
												window.open(`/api/files/stream?path=${encodeURIComponent(targetPath)}&download=true&token=${encodeURIComponent(token || '')}`, '_blank');
											}}
											style={{ display: 'flex', alignItems: 'center', gap: 6 }}
										>
											<FiExternalLink size={14} /> Direct URL
										</button>
									</div>
								</div>
							)}

							{/* Code / Text Editor */}
							{previewType === 'editor' && (
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
									{editorLoading ? (
										<div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', flex: 1 }}>
											<div style={{ width: 22, height: 22, border: '2px solid var(--color-brand-border)', borderTopColor: 'var(--color-brand)', borderRadius: '50%', animation: 'spin .6s linear infinite' }} />
										</div>
									) : (
										<div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
											<div style={{ flex: 1, border: '1px solid var(--color-brand-border)', borderRadius: 8, overflow: 'hidden', background: 'var(--color-brand-bg)' }}>
												<Editor
													height="100%"
													language={editorLang}
													theme="vs-dark"
													value={editorContent}
													onChange={(value) => setEditorContent(value || '')}
													options={{
														minimap: { enabled: false },
														fontSize: 12,
														lineNumbers: 'on',
														scrollBeyondLastLine: false,
														automaticLayout: true
													}}
													loading={
														<textarea
															value={editorContent}
															onChange={(e) => setEditorContent(e.target.value)}
															style={{
																width: '100%',
																height: '100%',
																fontFamily: 'monospace',
																fontSize: 12,
																padding: 10,
																background: '#1c1c1c',
																color: '#d4d4d4',
																border: 'none',
																resize: 'none',
																outline: 'none'
															}}
														/>
													}
												/>
											</div>
											<button 
												className="btn btn--primary" 
												onClick={handleSaveContent} 
												disabled={savingContent}
												style={{ marginTop: 10, display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6 }}
											>
												<FiCheck size={14} /> {savingContent ? 'Saving...' : 'Save Changes'}
											</button>
										</div>
									)}
								</div>
							)}

							{/* Properties Metadata table if no preview type */}
							{previewType === 'other' && (
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 12 }}>
									<div style={{ display: 'flex', justifyContent: 'center', padding: '16px 0' }}>
										<FiFile size={56} style={{ color: 'var(--color-brand-text)' }} />
									</div>
									<div style={{ background: 'var(--color-brand-bg)', borderRadius: 8, padding: 14, border: '1px solid var(--color-brand-border)' }}>
										<h4 style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', marginBottom: 8, letterSpacing: '0.05em' }}>Metadata Info</h4>
										<table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
											<tbody>
												<tr style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
													<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>Type</td>
													<td style={{ padding: '6px 0', textAlign: 'right', color: 'var(--color-brand-heading)' }}>{previewFile.extension.toUpperCase() || 'Binary'}</td>
												</tr>
												<tr style={{ borderBottom: '1px solid var(--color-brand-border)' }}>
													<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>Size</td>
													<td style={{ padding: '6px 0', textAlign: 'right', color: 'var(--color-brand-heading)' }}>{formatSize(previewFile.size)}</td>
												</tr>
												<tr>
													<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>Modified</td>
													<td style={{ padding: '6px 0', textAlign: 'right', color: 'var(--color-brand-heading)' }}>{new Date(previewFile.mod_time).toLocaleString()}</td>
												</tr>
												{previewFile.s3_key && (
													<tr>
														<td style={{ padding: '6px 0', color: 'var(--color-brand-text)', fontWeight: 600 }}>Storage</td>
														<td style={{ padding: '6px 0', textAlign: 'right' }}>
															<span style={{ display: 'inline-flex', alignItems: 'center', gap: 3, color: '#3b82f6', fontWeight: 600, fontSize: 11 }}>
																<FiCloud size={11} /> S3 Object
															</span>
														</td>
													</tr>
												)}
											</tbody>
										</table>
									</div>
									<a 
										href={`/api/files/stream?path=${encodeURIComponent(currentPath === '/' ? `/${previewFile.name}` : `${currentPath}/${previewFile.name}`)}&download=true&token=${encodeURIComponent(token || '')}`} 
										download
										className="btn btn--primary"
										style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6, marginTop: 10 }}
									>
										<FiDownload size={14} /> Download File
									</a>
								</div>
							)}
						</div>
					</div>
				)}
			</div>

			{/* Global Physical Search Modal */}
			{showSearchModal && (
				<div style={{ position: 'fixed', left: 0, top: 0, width: '100vw', height: '100vh', background: 'rgba(10,12,18,0.6)', backdropFilter: 'blur(8px)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 999 }}>
					<div className="g-card animate-zoom-in" style={{ width: 650, maxHeight: '80vh', display: 'flex', flexDirection: 'column', padding: 22, boxShadow: '0 20px 45px rgba(0,0,0,0.3)', border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-card)' }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 }}>
							<div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
								<FiSearch size={18} style={{ color: 'var(--color-brand)' }} />
								<h3 style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>Physical Disk Search</h3>
							</div>
							<button onClick={() => setShowSearchModal(false)} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: 4, color: 'var(--color-brand-heading)', display: 'flex' }}><FiX size={18} /></button>
						</div>
						
						<div style={{ position: 'relative', marginBottom: 14 }}>
							<input 
								type="text" 
								required
								autoFocus
								value={globalSearchQuery}
								onChange={(e) => setGlobalSearchQuery(e.target.value)}
								placeholder="Search files physically on disk (type more than 3 characters)..."
								style={{ width: '100%', padding: '10px 14px 10px 36px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13, outline: 'none', transition: 'border-color 0.2s' }}
							/>
							<FiSearch size={14} style={{ position: 'absolute', left: 12, top: 13, color: 'var(--color-brand-muted)' }} />
							{globalSearchLoading && (
								<div style={{ position: 'absolute', right: 12, top: 12 }}>
									<div style={{ width: 14, height: 14, border: '2px solid var(--color-brand-border)', borderTopColor: 'var(--color-brand)', borderRadius: '50%', animation: 'spin .6s linear infinite' }} />
								</div>
							)}
						</div>

						{/* Results Area */}
						<div style={{ flex: 1, overflowY: 'auto', minHeight: 250, maxHeight: 400, display: 'flex', flexDirection: 'column', gap: 6 }}>
							{globalSearchQuery.trim().length <= 3 ? (
								<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', flex: 1, gap: 8, color: 'var(--color-brand-muted)', padding: '40px 0' }}>
									<FiInfo size={24} />
									<span style={{ fontSize: 13 }}>Please enter more than 3 characters to search physically.</span>
								</div>
							) : globalSearchLoading ? (
								<div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', flex: 1, padding: '40px 0' }}>
									<div style={{ width: 24, height: 24, border: '2.5px solid var(--color-brand-border)', borderTopColor: 'var(--color-brand)', borderRadius: '50%', animation: 'spin .6s linear infinite' }} />
								</div>
							) : globalSearchResults.length === 0 ? (
								<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', flex: 1, gap: 8, color: 'var(--color-brand-muted)', padding: '40px 0' }}>
									<FiFolder size={24} />
									<span style={{ fontSize: 13 }}>No files found matching "{globalSearchQuery}".</span>
								</div>
							) : (
								<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
									<div style={{ fontSize: 11, fontWeight: 600, color: 'var(--color-brand-muted)', paddingBottom: 6, borderBottom: '1px solid var(--color-brand-border)', display: 'flex', justifyContent: 'space-between' }}>
										<span>Found {globalSearchResults.length} results</span>
										<span>Click to open location</span>
									</div>
									{globalSearchResults.map((result) => (
										<div 
											key={result.path}
											onClick={() => handleSearchResultClick(result)}
											style={{
												display: 'flex',
												alignItems: 'center',
												justifyContent: 'space-between',
												padding: '8px 12px',
												borderRadius: 8,
												background: 'var(--color-brand-card)',
												border: '1px solid var(--color-brand-border)',
												cursor: 'pointer',
												transition: 'all 0.12s'
											}}
											className="file-row-hover"
										>
											<div style={{ display: 'flex', alignItems: 'center', gap: 10, flex: 1, minWidth: 0 }}>
												<div style={{ display: 'flex', flexShrink: 0 }}>
													{result.is_dir ? <FiFolder style={{ color: '#eab308', fontSize: 18 }} /> : <FiFile style={{ color: '#64748b', fontSize: 18 }} />}
												</div>
												<div style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
													<span style={{ fontSize: 13, fontWeight: 600, color: 'var(--color-brand-heading)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
														{result.name}
													</span>
													<span style={{ fontSize: 10, color: 'var(--color-brand-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
														{result.path}
													</span>
												</div>
											</div>
											<div style={{ display: 'flex', alignItems: 'center', gap: 16, fontSize: 11, color: 'var(--color-brand-text)', flexShrink: 0 }}>
												<span>{result.is_dir ? 'DIR' : formatSize(result.size)}</span>
												<span style={{ color: 'var(--color-brand)', fontWeight: 600 }}>Open →</span>
											</div>
										</div>
									))}
								</div>
							)}
						</div>
					</div>
				</div>
			)}

			{/* Create Folder Modal */}
			{showFolderModal && (
				<div style={{ position: 'fixed', left: 0, top: 0, width: '100vw', height: '100vh', background: 'rgba(0,0,0,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 999 }}>
					<div className="g-card animate-zoom-in" style={{ width: 360, padding: 22, boxShadow: '0 20px 40px rgba(0,0,0,0.2)' }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 }}>
							<h3 style={{ fontSize: 15, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>Create New Folder</h3>
							<button onClick={() => setShowFolderModal(false)} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: 0 }}><FiX size={16} /></button>
						</div>
						<form onSubmit={handleCreateFolder}>
							<input 
								type="text" 
								required
								value={newFolderName}
								onChange={(e) => setNewFolderName(e.target.value)}
								placeholder="Enter folder name..."
								style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13, marginBottom: 14 }}
							/>
							<div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
								<button type="button" className="btn" onClick={() => setShowFolderModal(false)}>Cancel</button>
								<button type="submit" className="btn btn--primary" disabled={creatingFolder}>{creatingFolder ? 'Creating...' : 'Create Folder'}</button>
							</div>
						</form>
					</div>
				</div>
			)}

			{/* Upload to Cloud Modal (rclone) */}
			{showCloudUploadModal && (
				<div style={{ position: 'fixed', left: 0, top: 0, width: '100vw', height: '100vh', background: 'rgba(0,0,0,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 999 }}>
					<div className="g-card animate-zoom-in" style={{ width: 420, padding: 22, boxShadow: '0 20px 40px rgba(0,0,0,0.2)' }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 }}>
							<h3 style={{ fontSize: 15, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>Upload to Cloud</h3>
							<button onClick={() => setShowCloudUploadModal(false)} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: 0 }}><FiX size={16} /></button>
						</div>
						<form onSubmit={handleCloudUpload}>
							<div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginBottom: 14 }}>
								<div>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', display: 'block', marginBottom: 4 }}>Remote</label>
									<select 
										value={cloudRemoteId} 
										onChange={(e) => setCloudRemoteId(e.target.value)}
										style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13 }}
									>
										{cloudRemotes.map((r) => (
<option key={r.id} value={String(r.id)}>{r.name} ({r.type})</option>
										))}
									</select>
								</div>
								<div>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', display: 'block', marginBottom: 4 }}>Destination directory (optional)</label>
									<input 
										type="text" 
										value={cloudDestDir}
										onChange={(e) => setCloudDestDir(e.target.value)}
										placeholder="e.g. uploads/2026"
										style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13 }}
									/>
									<div style={{ fontSize: 10.5, color: 'var(--color-brand-muted)', marginTop: 3 }}>Added under the remote's root prefix.</div>
</div>
<label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12, color: 'var(--color-brand-text)', cursor: 'pointer' }}>
<input type="checkbox" checked={cloudKeepStructure} onChange={(e) => setCloudKeepStructure(e.target.checked)} />
Keep folder structure (mirror the local path)
</label>
<label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12, color: 'var(--color-brand-text)', cursor: 'pointer' }}>
<input type="checkbox" checked={cloudNeedLink} onChange={(e) => setCloudNeedLink(e.target.checked)} />
Request public share links
</label>
<div style={{ fontSize: 11, color: 'var(--color-brand-muted)' }}>
{selectedItems.length} selected item(s) will be uploaded through the job scheduler.
</div>
</div>
<div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
<button type="button" className="btn" onClick={() => setShowCloudUploadModal(false)}>Cancel</button>
<button type="submit" className="btn btn--primary" disabled={cloudUploading}>{cloudUploading ? 'Queuing...' : 'Upload to Cloud'}</button>
</div>
</form>
</div>
</div>
)}

			{/* Archive Modal */}
			{showArchiveModal && (
				<div style={{ position: 'fixed', left: 0, top: 0, width: '100vw', height: '100vh', background: 'rgba(0,0,0,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 999 }}>
					<div className="g-card animate-zoom-in" style={{ width: 360, padding: 22, boxShadow: '0 20px 40px rgba(0,0,0,0.2)' }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 }}>
							<h3 style={{ fontSize: 15, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>Compress Selected</h3>
							<button onClick={() => setShowArchiveModal(false)} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: 0 }}><FiX size={16} /></button>
						</div>
						<form onSubmit={handleCompress}>
							<div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginBottom: 14 }}>
								<div>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', display: 'block', marginBottom: 4 }}>Format</label>
									<select 
										value={archiveFormat} 
										onChange={(e) => {
											const fmt = e.target.value;
											setArchiveFormat(fmt);
											setArchiveName(prev => updateArchiveExtension(prev, fmt));
										}}
										style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13 }}
									>
										<option value=".zip">ZIP Archive (.zip)</option>
										<option value=".tar">TAR Archive (.tar)</option>
										<option value=".tar.gz">TAR GZIP (.tar.gz)</option>
										<option value=".tar.bz2">TAR BZIP2 (.tar.bz2)</option>
										<option value=".tar.xz">TAR XZ (.tar.xz)</option>
										<option value=".tar.zst">TAR Zstandard (.tar.zst)</option>
										<option value=".rar">RAR Archive (.rar)</option>
										<option value=".7z">7Z Archive (.7z)</option>
									</select>
								</div>
								<div>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase', display: 'block', marginBottom: 4 }}>Archive Name</label>
									<input 
										type="text" 
										required
										value={archiveName}
										onChange={(e) => setArchiveName(e.target.value)}
										placeholder="archive.zip"
										style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13 }}
									/>
								</div>
							</div>
							<div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
								<button type="button" className="btn" onClick={() => setShowArchiveModal(false)}>Cancel</button>
								<button type="submit" className="btn btn--primary" disabled={compressing}>{compressing ? 'Archiving...' : 'Compress Items'}</button>
							</div>
						</form>
					</div>
				</div>
			)}

			{/* Archive Password Modal */}
			{passwordModal.show && (
				<div style={{ position: 'fixed', left: 0, top: 0, width: '100vw', height: '100vh', background: 'rgba(0,0,0,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 999 }}>
					<div className="g-card animate-zoom-in" style={{ width: 360, padding: 22, boxShadow: '0 20px 40px rgba(0,0,0,0.2)' }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 }}>
							<h3 style={{ fontSize: 15, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>Archive Password Required</h3>
							<button onClick={() => setPasswordModal({ show: false, targetPath: '' })} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: 0 }}><FiX size={16} /></button>
						</div>
						<form onSubmit={(e) => {
							e.preventDefault();
							if (passwordModal.callback) {
								passwordModal.callback(archivePassword);
							}
							setPasswordModal({ show: false, targetPath: '' });
							setArchivePassword('');
						}}>
							<p style={{ fontSize: 13, color: 'var(--color-brand-text)', marginBottom: 12, lineHeight: '1.4' }}>
								The archive <strong>{passwordModal.targetPath.split('/').pop()}</strong> is password-protected. Please enter its password:
							</p>
							<input 
								type="password" 
								required
								autoFocus
								value={archivePassword}
								onChange={(e) => setArchivePassword(e.target.value)}
								placeholder="Enter archive password..."
								style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13, marginBottom: 14 }}
							/>
							<div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
								<button type="button" className="btn" onClick={() => {
									setPasswordModal({ show: false, targetPath: '' });
									setArchivePassword('');
								}}>Cancel</button>
								<button type="submit" className="btn btn--primary">Decrypt & Extract</button>
							</div>
						</form>
					</div>
				</div>
			)}

			{/* Compress Queue Modal */}
			{showCompressQueueModal && (
				<div style={{ position: 'fixed', left: 0, top: 0, width: '100vw', height: '100vh', background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 9999, backdropFilter: 'blur(4px)' }}>
					<div className="g-card animate-zoom-in" style={{ width: 480, padding: 22, boxShadow: '0 20px 50px rgba(0,0,0,0.4)', border: '1px solid var(--color-brand-border)', maxHeight: '85vh', display: 'flex', flexDirection: 'column' }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 }}>
							<h3 style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>Compress Queue ({compressQueue.length} items)</h3>
							<button onClick={() => setShowCompressQueueModal(false)} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: 0 }}><FiX size={18} /></button>
						</div>
						
						<div style={{ flex: 1, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 14, minHeight: 120, maxHeight: 320, paddingRight: 4 }}>
							{compressQueue.length === 0 ? (
								<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--color-brand-muted)', gap: 10, padding: '20px 0' }}>
									<FiArchive size={32} style={{ opacity: 0.5 }} />
									<span style={{ fontSize: 12 }}>No files added to the compression list yet.</span>
								</div>
							) : (
								compressQueue.map((item, idx) => (
									<div key={idx} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '8px 10px', borderRadius: 6, background: 'var(--color-brand-bg)', border: '1px solid var(--color-brand-border)', gap: 10 }}>
										<div style={{ display: 'flex', flexDirection: 'column', overflow: 'hidden', flex: 1 }}>
											<span style={{ fontSize: 12, fontWeight: 600, color: 'var(--color-brand-heading)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{item.name}</span>
											<span style={{ fontSize: 10, color: 'var(--color-brand-muted)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{item.path}</span>
										</div>
										<button 
											className="btn btn--sm" 
											onClick={() => setCompressQueue(prev => prev.filter((_, i) => i !== idx))}
											style={{ padding: '3px 6px', fontSize: 10, color: 'var(--color-brand-red, #ef4444)', background: 'rgba(239,68,68,0.05)', border: '1px solid rgba(239,68,68,0.15)' }}
										>
											Remove
										</button>
									</div>
								))
							)}
						</div>
						
						{compressQueue.length > 0 ? (
							<div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
								<div style={{ display: 'flex', gap: 10 }}>
									<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
										<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase' }}>Format</label>
										<select 
											value={archiveFormat} 
											onChange={(e) => {
												const fmt = e.target.value;
												setArchiveFormat(fmt);
												setArchiveName(prev => updateArchiveExtension(prev, fmt));
											}}
											style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13 }}
										>
											<option value=".zip">ZIP (.zip)</option>
											<option value=".tar">TAR (.tar)</option>
											<option value=".tar.gz">TAR.GZ (.tar.gz)</option>
											<option value=".tar.bz2">TAR.BZ2 (.tar.bz2)</option>
											<option value=".tar.xz">TAR.XZ (.tar.xz)</option>
											<option value=".tar.zst">TAR.ZST (.tar.zst)</option>
											<option value=".rar">RAR (.rar)</option>
											<option value=".7z">7Z (.7z)</option>
										</select>
									</div>
									<div style={{ flex: 2, display: 'flex', flexDirection: 'column', gap: 4 }}>
										<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)', textTransform: 'uppercase' }}>Archive Name</label>
										<input 
											type="text" 
											required
											value={archiveName}
											onChange={(e) => setArchiveName(e.target.value)}
											placeholder="archive.zip"
											style={{ width: '100%', padding: '8px 10px', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', fontSize: 13 }}
										/>
									</div>
								</div>
								
								<div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 10 }}>
									<button type="button" className="btn btn--secondary btn--sm" onClick={() => setCompressQueue([])}>Clear All</button>
									<button type="button" className="btn btn--sm" onClick={() => setShowCompressQueueModal(false)}>Close</button>
									<button 
										type="button" 
										className="btn btn--primary btn--sm" 
										disabled={compressing || !archiveName.trim()}
										onClick={handleCompressQueue}
									>
										{compressing ? 'Archiving...' : 'Compress'}
									</button>
								</div>
							</div>
						) : (
							<div style={{ display: 'flex', justifyContent: 'flex-end' }}>
								<button type="button" className="btn btn--sm" onClick={() => setShowCompressQueueModal(false)}>Close</button>
							</div>
						)}
					</div>
				</div>
			)}

			{/* Share Link Modal */}
			{showShareModal && (
				<div style={{ position: 'fixed', left: 0, top: 0, width: '100vw', height: '100vh', background: 'rgba(0,0,0,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 999 }}>
					<div className="g-card animate-zoom-in" style={{ width: 440, padding: 22, boxShadow: '0 20px 40px rgba(0,0,0,0.2)' }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 }}>
							<h3 style={{ fontSize: 15, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>Temporary Direct Link</h3>
							<button onClick={() => setShowShareModal(false)} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: 0 }}><FiX size={16} /></button>
						</div>
						<div style={{ fontSize: 12, color: 'var(--color-brand-text)', marginBottom: 10 }}>
							This sharing URL contains an authenticated 24h temporary token. Share with external downloading agents:
						</div>
						<textarea 
							readOnly
							value={shareUrl}
							style={{ width: '100%', height: 75, padding: 8, fontSize: 11, fontFamily: 'monospace', borderRadius: 6, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', resize: 'none', outline: 'none', marginBottom: 14 }}
						/>
						<div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
							<button 
								onClick={() => { navigator.clipboard.writeText(shareUrl); showGlobalAlert('URL copied to clipboard!', { title: 'Copied', variant: 'success' }); }} 
								className="btn btn--primary"
							>
								Copy to Clipboard
							</button>
							<button className="btn" onClick={() => setShowShareModal(false)}>Close</button>
						</div>
					</div>
				</div>
			)}

			{/* Advanced Lightbox Gallery Overlay */}
			{isLightboxOpen && imageFiles.length > 0 && (
				<div 
					style={{
						position: 'fixed',
						top: 0,
						left: 0,
						width: '100vw',
						height: '100vh',
						background: 'rgba(10,12,18,0.96)',
						backdropFilter: 'blur(16px)',
						zIndex: 9999,
						display: 'flex',
						flexDirection: 'column',
						color: '#fff',
						animation: 'fadeIn 0.25s ease-out'
					}}
				>
					<style>{`
						@keyframes fadeIn {
							from { opacity: 0; }
							to { opacity: 1; }
						}
						@keyframes slideFadeIn {
							from { opacity: 0; transform: scale(0.97) translateY(8px); }
							to { opacity: 1; transform: scale(1) translateY(0); }
						}
						.lightbox-arrow:hover {
							background: rgba(255,255,255,0.18) !important;
							transform: scale(1.05);
						}
					`}</style>

					{/* Top Header Bar */}
					<div 
						style={{
							display: 'flex',
							justifyContent: 'space-between',
							alignItems: 'center',
							padding: '16px 24px',
							background: 'linear-gradient(to bottom, rgba(0,0,0,0.5), transparent)',
							borderBottom: '1px solid rgba(255,255,255,0.05)'
						}}
					>
						<div style={{ display: 'flex', flexDirection: 'column' }}>
							<span style={{ fontSize: 14, fontWeight: 700, letterSpacing: '0.2px' }}>
								{imageFiles[lightboxIndex]?.name}
							</span>
							<span style={{ fontSize: 11, color: 'rgba(255,255,255,0.4)', marginTop: 2 }}>
								Image {lightboxIndex + 1} of {imageFiles.length} • {formatSize(imageFiles[lightboxIndex]?.size)}
							</span>
						</div>

						{/* Action Buttons */}
						<div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
							{/* Slideshow Play/Pause */}
							<button 
								onClick={() => setIsPlaying(prev => !prev)}
								style={{
									background: isPlaying ? 'var(--color-brand)' : 'rgba(255,255,255,0.08)',
									border: 'none',
									borderRadius: 20,
									padding: '6px 14px',
									color: '#fff',
									fontSize: 12,
									fontWeight: 600,
									display: 'flex',
									alignItems: 'center',
									gap: 6,
									cursor: 'pointer',
									transition: 'background 0.2s'
								}}
							>
								{isPlaying ? <FiPause size={12} /> : <FiPlay size={12} />}
								{isPlaying ? 'Pause Slideshow' : 'Start Slideshow'}
							</button>

							{/* Fullscreen API Trigger */}
							<button 
								onClick={() => {
									const container = document.getElementById('lightbox-image-stage');
									if (container) {
										if (!document.fullscreenElement) {
											container.requestFullscreen().catch(err => console.error(err));
										} else {
											document.exitFullscreen();
										}
									}
								}}
								style={{
									background: 'rgba(255,255,255,0.08)',
									border: 'none',
									borderRadius: '50%',
									width: 34,
									height: 34,
									display: 'flex',
									alignItems: 'center',
									justifyContent: 'center',
									color: '#fff',
									cursor: 'pointer',
									transition: 'background 0.2s'
								}}
								title="Fullscreen"
							>
								<FiMaximize2 size={14} />
							</button>

							{/* Close Button */}
							<button 
								onClick={() => {
									setIsLightboxOpen(false);
									setIsPlaying(false);
								}}
								style={{
									background: 'rgba(239,68,68,0.15)',
									border: 'none',
									borderRadius: '50%',
									width: 34,
									height: 34,
									display: 'flex',
									alignItems: 'center',
									justifyContent: 'center',
									color: '#ef4444',
									cursor: 'pointer',
									transition: 'background 0.2s'
								}}
								title="Close Lightbox"
							>
								<FiX size={16} />
							</button>
						</div>
					</div>

					{/* Main Display Stage */}
					<div 
						id="lightbox-image-stage"
						style={{
							flex: 1,
							display: 'flex',
							alignItems: 'center',
							justifyContent: 'center',
							position: 'relative',
							overflow: 'hidden',
							background: '#000'
						}}
					>
						{/* Left Arrow Button */}
						<button 
							onClick={() => setLightboxIndex(prev => (prev - 1 + imageFiles.length) % imageFiles.length)}
							style={{
								position: 'absolute',
								left: 20,
								zIndex: 10,
								background: 'rgba(255,255,255,0.08)',
								border: 'none',
								borderRadius: '50%',
								width: 48,
								height: 48,
								display: 'flex',
								alignItems: 'center',
								justifyContent: 'center',
								color: '#fff',
								cursor: 'pointer',
								transition: 'all 0.2s'
							}}
							className="lightbox-arrow"
						>
							<FiChevronLeft size={24} />
						</button>

						{/* Image Container with dynamic keys to trigger keyframe transition animations */}
						<div 
							key={lightboxIndex} 
							style={{
								maxWidth: '90%',
								maxHeight: '90%',
								display: 'flex',
								alignItems: 'center',
								justifyContent: 'center',
								animation: 'slideFadeIn 0.35s cubic-bezier(0.16, 1, 0.3, 1)'
							}}
						>
							<img 
								src={`/api/files/stream?path=${encodeURIComponent(currentPath === '/' ? `/${imageFiles[lightboxIndex]?.name}` : `${currentPath}/${imageFiles[lightboxIndex]?.name}`)}&token=${encodeURIComponent(token || '')}`} 
								alt={imageFiles[lightboxIndex]?.name}
								style={{
									maxWidth: '100%',
									maxHeight: '100%',
									objectFit: 'contain',
									boxShadow: '0 12px 40px rgba(0,0,0,0.8)',
									borderRadius: 6
								}}
							/>
						</div>

						{/* Right Arrow Button */}
						<button 
							onClick={() => setLightboxIndex(prev => (prev + 1) % imageFiles.length)}
							style={{
								position: 'absolute',
								right: 20,
								zIndex: 10,
								background: 'rgba(255,255,255,0.08)',
								border: 'none',
								borderRadius: '50%',
								width: 48,
								height: 48,
								display: 'flex',
								alignItems: 'center',
								justifyContent: 'center',
								color: '#fff',
								cursor: 'pointer',
								transition: 'all 0.2s'
							}}
							className="lightbox-arrow"
						>
							<FiChevronRight size={24} />
						</button>
					</div>

					{/* Bottom Thumbnail Strip - Gallery */}
					<div 
						style={{
							padding: '16px 20px',
							background: 'rgba(10,12,18,0.98)',
							borderTop: '1px solid rgba(255,255,255,0.05)',
							display: 'flex',
							justifyContent: 'center',
							alignItems: 'center'
						}}
					>
						<div 
							style={{
								display: 'flex',
								gap: 10,
								overflowX: 'auto',
								maxWidth: '90%',
								padding: '4px 0',
								scrollbarWidth: 'thin'
							}}
						>
							{imageFiles.map((file, idx) => {
								const isSelected = idx === lightboxIndex;
								const pathStr = currentPath === '/' ? `/${file.name}` : `${currentPath}/${file.name}`;
								return (
									<div 
										key={file.name}
										onClick={() => {
											setLightboxIndex(idx);
											setIsPlaying(false);
										}}
										style={{
											width: 60,
											height: 60,
											borderRadius: 6,
											overflow: 'hidden',
											cursor: 'pointer',
											border: isSelected ? '2.5px solid var(--color-brand)' : '1.5px solid rgba(255,255,255,0.15)',
											opacity: isSelected ? 1 : 0.5,
											transition: 'all 0.2s',
											flexShrink: 0,
											transform: isSelected ? 'scale(1.08)' : 'scale(1)'
										}}
									>
										<img 
											src={`/api/files/stream?path=${encodeURIComponent(pathStr)}&token=${encodeURIComponent(token || '')}`}
											alt={file.name}
											loading="lazy" 
											style={{
												width: '100%',
												height: '100%',
												objectFit: 'cover'
											}}
										/>
									</div>
								);
							})}
						</div>
					</div>
				</div>
			)}

			{showVideoModal && videoModalFile && (
				<div 
					style={{ 
						position: 'fixed', 
						left: 0, 
						top: 0, 
						width: '100vw', 
						height: '100vh', 
						background: 'rgba(0,0,0,0.85)', 
						backdropFilter: 'blur(8px)',
						display: 'flex', 
						alignItems: 'center', 
						justifyContent: 'center', 
						zIndex: 99999 
					}}
					onClick={() => setShowVideoModal(false)}
				>
					<div 
						className="g-card animate-zoom-in" 
						style={{ 
							width: detectedMetadata ? Math.max(480, Math.min(window.innerWidth * 0.9, detectedMetadata.width / 2)) : 640,
							height: detectedMetadata ? Math.max(360, Math.min(window.innerHeight * 0.9, detectedMetadata.height / 2)) : 360,
							padding: 0, 
							background: '#000',
							border: '1px solid rgba(255,255,255,0.15)',
							boxShadow: '0 25px 60px rgba(0,0,0,0.85)',
							borderRadius: 12,
							overflow: 'hidden',
							display: 'flex',
							flexDirection: 'column'
						}}
						onClick={(e) => e.stopPropagation()}
					>
						<CustomVideoPlayer 
							src={`/api/files/stream?path=${encodeURIComponent(currentPath === '/' ? `/${videoModalFile.name}` : `${currentPath}/${videoModalFile.name}`)}&token=${encodeURIComponent(token || '')}`}
							title={videoModalFile.name}
							onClose={() => setShowVideoModal(false)}
						/>
					</div>
				</div>
			)}

			{contextMenu.visible && contextMenu.file && (
				<div 
					style={{
						position: 'fixed',
						top: contextMenu.y,
						left: contextMenu.x,
						background: 'var(--color-brand-card, #1e1e2d)',
						border: '1px solid var(--color-brand-border, #2b2b40)',
						borderRadius: 8,
						boxShadow: '0 10px 25px rgba(0,0,0,0.5)',
						zIndex: 100000,
						padding: '4px 0',
						minWidth: 180,
						fontFamily: 'system-ui, sans-serif'
					}}
					onClick={(e) => e.stopPropagation()}
				>
					{contextMenu.file.is_dir ? (
						<div 
							className="context-menu-item" 
							onClick={() => { navigateTo(contextMenu.file!.name); setContextMenu({ ...contextMenu, visible: false }); }}
							style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
						>
							<FiFolder size={14} style={{ color: '#eab308' }} /> Open Folder
						</div>
					) : (
						<div 
							className="context-menu-item" 
							onClick={() => { openPreview(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
							style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
						>
							<FiZoomIn size={14} style={{ color: 'var(--color-brand, #3b82f6)' }} /> Preview File
						</div>
					)}
					
					{!contextMenu.file.is_dir && (
						<>
							<div 
								className="context-menu-item" 
								onClick={() => { handleSendToTelegram(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
								style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
							>
								<FiShare2 size={14} style={{ color: '#10b981' }} /> Send to Telegram Bot
							</div>
							<div 
								className="context-menu-item" 
								onClick={() => { handleShareLink(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
								style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
							>
							<FiDownload size={14} style={{ color: '#a855f7' }} /> Get Download Link
						</div>
						{s3Enabled && !contextMenu.file.s3_key && (
							<div
								className="context-menu-item"
								onClick={() => { handleMoveToS3(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
								style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
							>
								<FiCloud size={14} style={{ color: '#3b82f6' }} /> Move to S3
							</div>
						)}
					</>
				)}

					<div 
						className="context-menu-item" 
						onClick={() => { handleAddToCompressQueue(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
						style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
					>
						<FiArchive size={14} style={{ color: '#6366f1' }} /> Add to Compress List
					</div>

					{!contextMenu.file.is_dir && isArchiveFile(contextMenu.file.name) && (
						<div 
							className="context-menu-item" 
							onClick={() => { handleExtract(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
							style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
						>
							<FiArchive size={14} style={{ color: '#3b82f6' }} /> Uncompress
						</div>
					)}

					<div style={{ height: 1, background: 'var(--color-brand-border, #2b2b40)', margin: '4px 0' }} />

					<div 
						className="context-menu-item" 
						onClick={() => { handleRename(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
						style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
					>
						<FiEdit3 size={14} /> Rename
					</div>

					<div 
						className="context-menu-item" 
						onClick={() => { handleCopySingle(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
						style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
					>
						<FiCopy size={14} /> Copy
					</div>

					<div 
						className="context-menu-item" 
						onClick={() => { handleCutSingle(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
						style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: 'var(--color-brand-heading)' }}
					>
						<FiScissors size={14} /> Cut
					</div>

					<div style={{ height: 1, background: 'var(--color-brand-border, #2b2b40)', margin: '4px 0' }} />

					<div 
						className="context-menu-item" 
						onClick={() => { handleDelete(contextMenu.file!); setContextMenu({ ...contextMenu, visible: false }); }}
						style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', fontSize: 12, cursor: 'pointer', color: '#ef4444' }}
					>
						<FiTrash2 size={14} /> Delete
					</div>
				</div>
			)}

			<style>{`
				.spin-anim {
					animation: spin 1s linear infinite;
				}
				@keyframes spin {
					to { transform: rotate(360deg); }
				}
				.file-card-hover:hover {
					border-color: var(--color-brand) !important;
					transform: translateY(-2px);
					box-shadow: 0 4px 12px rgba(0,0,0,0.05);
				}
				.file-row-hover:hover {
					background: rgba(99, 102, 241, 0.02) !important;
					border-color: var(--color-brand-border) !important;
				}
				.context-menu-item:hover {
					background: rgba(99, 102, 241, 0.08) !important;
				}
			`}</style>
		</div>
	);
};
