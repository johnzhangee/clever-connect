import React, { useState, useEffect } from 'react';
import { useAuthStore } from '../store/authStore';
import { useJobsStore } from '../store/jobsStore';
import { showGlobalAlert } from '../store/dialogStore';
import { 
	FiPlay, FiPause, FiTrash2, FiFolder, FiPlus, FiSettings, 
	FiGlobe, FiCheck, FiX, FiLink, FiDownloadCloud, FiServer,
	FiChevronLeft, FiFolderPlus, FiInfo, FiDownload, FiLayers
} from 'react-icons/fi';

interface TorrentJob {
	info_hash: string;
	name: string;
	magnet_uri: string;
	torrent_path: string;
	save_directory: string;
	status: string;
	total_bytes: number;
	downloaded: number;
	uploaded: number;
	progress: number;
	download_speed: number;
	upload_speed: number;
	peers: number;
	error_message: string;
	created_at: string;
}

interface TorrentFileItem {
	index: number;
	path: string;
	length: number;
	completed: number;
	percentage: number;
}

const formatBytes = (bytes: number, decimals = 2) => {
	if (bytes === 0) return '0 Bytes';
	const k = 1024;
	const dm = decimals < 0 ? 0 : decimals;
	const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
	const i = Math.floor(Math.log(bytes) / Math.log(k));
	return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
};

const formatSpeed = (mbps: number) => {
	if (mbps < 0.1) {
		return (mbps * 1024).toFixed(1) + ' KB/s';
	}
	return mbps.toFixed(2) + ' MB/s';
};

const TorrentProgressBar: React.FC<{ progress: number; status: string }> = ({ progress, status }) => {
	const isDownloading = status === 'downloading';
	const isCompleted = status === 'completed' || status === 'seeding';
	const isPaused = status === 'paused';
	const isQueued = status === 'queued';
	const isIdle = isPaused || isQueued;

	const outerCircleColor = isCompleted ? '#166534' : isIdle ? '#374151' : '#ea580c';
	const trackBorderColor = isCompleted ? '#22c55e' : isIdle ? '#4b5563' : '#f97316';
	const fillGradient = isCompleted 
		? 'linear-gradient(180deg, #4ade80 0%, #22c55e 100%)' 
		: isIdle 
		? 'linear-gradient(180deg, #9ca3af 0%, #4b5563 100%)' 
		: 'linear-gradient(180deg, #fbbf24 0%, #ea580c 100%)';

	const lightningColor = isCompleted ? '#4ade80' : isIdle ? '#9ca3af' : '#38bdf8';

	return (
		<div style={{ display: 'flex', alignItems: 'center', position: 'relative', width: '100%', height: 44, margin: '6px 0' }}>
			{/* Left badge circle */}
			<div style={{
				width: 44,
				height: 44,
				borderRadius: '50%',
				background: `linear-gradient(135deg, ${trackBorderColor} 0%, ${outerCircleColor} 100%)`,
				border: '2px solid rgba(255,255,255,0.2)',
				boxShadow: '0 3px 6px rgba(0,0,0,0.3), inset 0 2px 4px rgba(255,255,255,0.4)',
				display: 'flex',
				alignItems: 'center',
				justifyContent: 'center',
				zIndex: 10,
				position: 'absolute',
				left: 0,
			}}>
				{/* Inner glass circle */}
				<div style={{
					width: 34,
					height: 34,
					borderRadius: '50%',
					background: isPaused ? 'linear-gradient(135deg, #e5e7eb 0%, #9ca3af 100%)' : 'linear-gradient(135deg, #fef08a 0%, #eab308 100%)',
					border: '1px solid rgba(255,255,255,0.6)',
					boxShadow: 'inset 0 1px 3px rgba(255,255,255,0.8), inset 0 -2px 4px rgba(0,0,0,0.2)',
					display: 'flex',
					alignItems: 'center',
					justifyContent: 'center',
					position: 'relative',
					overflow: 'hidden'
				}}>
					{/* Top glossy reflection overlay */}
					<div style={{
						position: 'absolute',
						top: 1,
						left: 4,
						right: 4,
						height: 12,
						background: 'linear-gradient(180deg, rgba(255,255,255,0.7) 0%, rgba(255,255,255,0) 100%)',
						borderRadius: '34px 34px 0 0',
						pointerEvents: 'none'
					}} />

					{/* Lightning Bolt SVG */}
					<svg 
						viewBox="0 0 24 24" 
						className={isDownloading ? 'pulse-lightning' : ''} 
						style={{ 
							width: 20, 
							height: 20, 
							fill: lightningColor,
							filter: `drop-shadow(0 1px 2px rgba(0,0,0,0.3))`
						}}
					>
						<path d="M19 9h-6l3-7L5 13h6l-3 9z" />
					</svg>
				</div>
			</div>

			{/* Right track wrapper */}
			<div style={{
				flex: 1,
				height: 28,
				background: '#27170a', // Dark warm brown-black track
				border: `2px solid ${trackBorderColor}`,
				borderRadius: 14,
				boxShadow: '0 2px 4px rgba(0,0,0,0.4), inset 0 2px 5px rgba(0,0,0,0.8)',
				padding: '2px',
				paddingLeft: 34, // Push starting point past circle boundary
				display: 'flex',
				alignItems: 'center',
				marginLeft: 10,
				position: 'relative',
				overflow: 'hidden'
			}}>
				{/* Inner progress fill bar */}
				<div style={{
					width: `${Math.max(3, progress)}%`, // min 3% to look nice inside track
					height: '100%',
					borderRadius: 10,
					background: fillGradient,
					position: 'relative',
					overflow: 'hidden',
					transition: 'width 0.4s cubic-bezier(0.4, 0, 0.2, 1)',
					boxShadow: '0 1px 2px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.4)'
				}}>
					{/* Animated Stripes overlay */}
					<div 
						className={isDownloading ? 'scroll-stripes' : ''}
						style={{
							position: 'absolute',
							top: 0,
							left: 0,
							right: 0,
							bottom: 0,
							backgroundImage: 'repeating-linear-gradient(45deg, rgba(255,255,255,0.18), rgba(255,255,255,0.18) 12px, transparent 12px, transparent 24px)',
							backgroundSize: '40px 100%'
						}}
					/>

					{/* Top gloss highlight bar overlay */}
					<div style={{
						position: 'absolute',
						top: 1,
						left: 4,
						right: 4,
						height: '40%',
						background: 'linear-gradient(180deg, rgba(255,255,255,0.5) 0%, rgba(255,255,255,0) 100%)',
						borderRadius: 6,
						pointerEvents: 'none'
					}} />
				</div>
			</div>
			
			<style>{`
				@keyframes pulse {
					0% { opacity: 0.6; transform: scale(0.95); }
					50% { opacity: 1; transform: scale(1.05); }
					100% { opacity: 0.6; transform: scale(0.95); }
				}
				.pulse-lightning {
					animation: pulse 1.2s infinite ease-in-out;
				}
				@keyframes scroll {
					from { background-position: 0 0; }
					to { background-position: 40px 0; }
				}
				.scroll-stripes {
					animation: scroll 1.5s linear infinite;
				}
				@keyframes spin {
					0% { transform: rotate(0deg); }
					100% { transform: rotate(360deg); }
				}
				.spinner {
					animation: spin 1s linear infinite;
				}
			`}</style>
		</div>
	);
};

interface TorrentConfig {
	save_directory: string;
	max_connections_per_torrent: number;
	max_half_open_connections: number;
	upload_limit_mb: number;
	download_limit_mb: number;
	enable_dht: boolean;
	enable_pex: boolean;
	enable_utp: boolean;
	enable_tcp: boolean;
	enable_upload: boolean;
	piece_hashers_per_torrent: number;
	custom_trackers: string;
}

export const TorrentPage: React.FC = () => {
	const torrents = useJobsStore(state => state.torrents);
	const initWebSocket = useJobsStore(state => state.initWebSocket);
	const sendAction = useJobsStore(state => state.sendAction);
	const { token } = useAuthStore();

	const [hoveredTorrentHash, setHoveredTorrentHash] = useState<string | null>(null);
	const [dismissedOverlays, setDismissedOverlays] = useState<Record<string, boolean>>({});

	const [showAddModal, setShowAddModal] = useState(false);
	const [magnetUri, setMagnetUri] = useState('');
	const [torrentFile, setTorrentFile] = useState<File | null>(null);
	const [saveDir, setSaveDir] = useState('./data/manager/downloads');
	const [showConfigModal, setShowConfigModal] = useState(false);
	const [folderPickerTarget, setFolderPickerTarget] = useState<'add' | 'settings'>('add');
	const [config, setConfig] = useState<TorrentConfig>({
		save_directory: './data/manager/downloads',
		max_connections_per_torrent: 200,
		max_half_open_connections: 100,
		upload_limit_mb: 0,
		download_limit_mb: 0,
		enable_dht: true,
		enable_pex: true,
		enable_utp: true,
		enable_tcp: true,
		enable_upload: true,
		piece_hashers_per_torrent: 4,
		custom_trackers: '',
	});

	// Folder Picker state
	const [showFolderModal, setShowFolderModal] = useState(false);
	const [currentPath, setCurrentPath] = useState('/');
	const [directories, setDirectories] = useState<string[]>([]);
	const [newFolderName, setNewFolderName] = useState('');
	const [showNewFolderInput, setShowNewFolderInput] = useState(false);

	// Files Select Modal state
	const [selectedTorrent, setSelectedTorrent] = useState<TorrentJob | null>(null);
	const [torrentFiles, setTorrentFiles] = useState<TorrentFileItem[]>([]);
	const [selectedFileIndices, setSelectedFileIndices] = useState<number[]>([]);
	const [loadingFiles, setLoadingFiles] = useState(false);

	// Delete confirmation
	const [torrentToDelete, setTorrentToDelete] = useState<TorrentJob | null>(null);
	const [deleteFilesOption, setDeleteFilesOption] = useState(false);

	// Add Torrent Step states
	const [addStep, setAddStep] = useState<'input' | 'submitting' | 'fetching_metadata' | 'select_files'>('input');
	const [addedInfoHash, setAddedInfoHash] = useState('');
	const [modalFiles, setModalFiles] = useState<TorrentFileItem[]>([]);
	const [selectedModalFileIndices, setSelectedModalFileIndices] = useState<number[]>([]);
	const [fileSearchQuery, setFileSearchQuery] = useState('');
	const [selectFilesEnabled, setSelectFilesEnabled] = useState(false);
	const magnetsCount = magnetUri.split(/[\r\n]+/).map(m => m.trim()).filter(Boolean).length;

	const fetchConfig = async () => {
		try {
			const res = await fetch('/api/torrent/config', {
				headers: { 'Authorization': `Bearer ${token}` }
			});
			if (res.ok) {
				const data = await res.json();
				setConfig(data);
				if (!saveDir) setSaveDir(data.save_directory);
			}
		} catch (err) {
			console.error('Failed to fetch torrent configuration', err);
		}
	};

	const handleSaveConfig = async () => {
		try {
			const res = await fetch('/api/torrent/config', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify(config)
			});
			if (res.ok) {
				setShowConfigModal(false);
				fetchConfig();
			} else {
				const data = await res.json();
				showGlobalAlert(data.error || 'Failed to save configuration', { title: 'Configuration Error', variant: 'error' });
			}
		} catch (err) {
			console.error(err);
		}
	};

	useEffect(() => {
		if (token) {
			const close = initWebSocket(token);
			fetchConfig();
			return () => close();
		}
	}, [token, initWebSocket]);

	// Fetch directories for picker
	const fetchDirectories = async (path: string) => {
		try {
			const res = await fetch(`/api/files/list?path=${encodeURIComponent(path)}`, {
				headers: { 'Authorization': `Bearer ${token}` }
			});
			if (res.ok) {
				const data = await res.json();
				const dirs = (data.files || [])
					.filter((f: any) => f.is_dir)
					.map((f: any) => f.name);
				setDirectories(dirs);
				setCurrentPath(data.current_path || '/');
			}
		} catch (err) {
			console.error('Failed to fetch folders', err);
		}
	};

	const openFolderPicker = (target: 'add' | 'settings') => {
		setFolderPickerTarget(target);
		const initialPath = target === 'add' ? saveDir : config.save_directory;
		fetchDirectories(initialPath || currentPath);
		setShowFolderModal(true);
	};

	const handleDirUp = () => {
		if (currentPath === '/' || currentPath === '') return;
		const parts = currentPath.split('/');
		parts.pop();
		const parent = parts.join('/') || '/';
		fetchDirectories(parent);
	};

	const handleDirSelect = (name: string) => {
		const next = currentPath === '/' ? `/${name}` : `${currentPath}/${name}`;
		fetchDirectories(next);
	};

	const handleCreateDirectory = async () => {
		if (!newFolderName.trim()) return;
		try {
			const res = await fetch('/api/files/create-folder', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({
					parent_path: currentPath,
					folder_name: newFolderName
				})
			});
			if (res.ok) {
				setNewFolderName('');
				setShowNewFolderInput(false);
				fetchDirectories(currentPath);
			} else {
				const err = await res.json();
				showGlobalAlert(`Failed: ${err.error || 'Unknown error'}`, { title: 'Directory Creation', variant: 'error' });
			}
		} catch (err) {
			console.error('Create folder error', err);
		}
	};

	const handleSelectAllFiles = () => {
		setSelectedModalFileIndices(modalFiles.map(f => f.index));
	};

	const handleDeselectAllFiles = () => {
		setSelectedModalFileIndices([]);
	};

	const handleToggleModalFile = (idx: number) => {
		setSelectedModalFileIndices(prev => 
			prev.includes(idx) ? prev.filter(i => i !== idx) : [...prev, idx]
		);
	};

	const handleCancelAdd = () => {
		if (addedInfoHash) {
			sendAction({ action: 'delete_torrent', info_hash: addedInfoHash, delete_files: true });
		}
		setAddStep('input');
		setAddedInfoHash('');
		setModalFiles([]);
		setSelectedModalFileIndices([]);
		setFileSearchQuery('');
		setSelectFilesEnabled(false);
		setShowAddModal(false);
	};

	const handleCloseKeepBackground = () => {
		setAddStep('input');
		setAddedInfoHash('');
		setModalFiles([]);
		setSelectedModalFileIndices([]);
		setFileSearchQuery('');
		setSelectFilesEnabled(false);
		setShowAddModal(false);
	};

	const handleConfirmDownload = async () => {
		if (!addedInfoHash) return;
		try {
			const res = await fetch('/api/torrent/select-files', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({
					info_hash: addedInfoHash,
					selected_files: selectedModalFileIndices
				})
			});
			if (res.ok) {
				sendAction({ action: 'resume_torrent', info_hash: addedInfoHash });
			}
		} catch (err) {
			console.error(err);
		} finally {
			setAddStep('input');
			setAddedInfoHash('');
			setModalFiles([]);
			setSelectedModalFileIndices([]);
			setFileSearchQuery('');
			setSelectFilesEnabled(false);
			setShowAddModal(false);
		}
	};

	const handleAddTorrent = async (e: React.FormEvent) => {
		e.preventDefault();
		if (!torrentFile && !magnetUri.trim()) return;

		const magnets = magnetUri.split(/[\r\n]+/).map(m => m.trim()).filter(Boolean);

		setAddStep('submitting');
		try {
			if (torrentFile) {
				const formData = new FormData();
				formData.append('file', torrentFile);
				formData.append('save_directory', saveDir);
				formData.append('select_files', selectFilesEnabled ? 'true' : 'false');
				const res = await fetch('/api/torrent/add', {
					method: 'POST',
					headers: { 'Authorization': `Bearer ${token}` },
					body: formData
				});
				if (res && res.ok) {
					const data = await res.json();
					if (data.info_hash) {
						if (selectFilesEnabled) {
							setAddedInfoHash(data.info_hash);
							setAddStep('fetching_metadata');
						} else {
							setAddStep('input');
							setTorrentFile(null);
							setMagnetUri('');
							setSelectFilesEnabled(false);
							setShowAddModal(false);
						}
					} else {
						setAddStep('input');
						showGlobalAlert('Failed to add torrent: info hash missing', { title: 'Torrent Error', variant: 'error' });
					}
				} else {
					const data = res ? await res.json() : {};
					setAddStep('input');
					showGlobalAlert(data.error || 'Failed to add torrent', { title: 'Torrent Error', variant: 'error' });
				}
			} else {
				const isBulk = magnets.length > 1;

				for (let i = 0; i < magnets.length; i++) {
					const magnet = magnets[i];
					const selectFiles = isBulk ? false : selectFilesEnabled;

					const res = await fetch('/api/torrent/add', {
						method: 'POST',
						headers: {
							'Content-Type': 'application/json',
							'Authorization': `Bearer ${token}`
						},
						body: JSON.stringify({
							magnet_uri: magnet,
							save_directory: saveDir,
							select_files: selectFiles
						})
					});

					if (res && res.ok) {
						const data = await res.json();
						if (!isBulk) {
							if (data.info_hash) {
								if (selectFiles) {
									setAddedInfoHash(data.info_hash);
									setAddStep('fetching_metadata');
									return;
								} else {
									setAddStep('input');
									setTorrentFile(null);
									setMagnetUri('');
									setSelectFilesEnabled(false);
									setShowAddModal(false);
								}
							} else {
								setAddStep('input');
								showGlobalAlert('Failed to add torrent: info hash missing', { title: 'Torrent Error', variant: 'error' });
							}
						}
					} else {
						const data = res ? await res.json() : {};
						if (!isBulk) {
							setAddStep('input');
							showGlobalAlert(data.error || 'Failed to add torrent', { title: 'Torrent Error', variant: 'error' });
							return;
						} else {
							console.error(`Failed to add magnet: ${magnet}`, data.error);
						}
					}
				}

				if (isBulk) {
					setAddStep('input');
					setTorrentFile(null);
					setMagnetUri('');
					setSelectFilesEnabled(false);
					setShowAddModal(false);
				}
			}
		} catch (err) {
			console.error(err);
			setAddStep('input');
			showGlobalAlert('Network error adding torrent', { title: 'Network Error', variant: 'error' });
		}
	};

	useEffect(() => {
		if (addStep !== 'fetching_metadata' || !addedInfoHash) return;

		let active = true;
		const poll = async () => {
			try {
				const res = await fetch(`/api/torrent/files?info_hash=${addedInfoHash}`, {
					headers: { 'Authorization': `Bearer ${token}` }
				});
				if (!active) return;
				if (res.ok) {
					const data = await res.json();
					if (data && data.status !== 'fetching_metadata') {
						setModalFiles(data || []);
						setSelectedModalFileIndices((data || []).map((f: TorrentFileItem) => f.index));
						setAddStep('select_files');
					}
				}
			} catch (err) {
				console.error('Failed to poll metadata files', err);
			}
		};

		poll();
		const interval = setInterval(poll, 1500);

		return () => {
			active = false;
			clearInterval(interval);
		};
	}, [addStep, addedInfoHash, token]);

	const handlePause = (infoHash: string) => {
		sendAction({ action: 'pause_torrent', info_hash: infoHash });
	};

	const handleResume = (infoHash: string) => {
		sendAction({ action: 'resume_torrent', info_hash: infoHash });
	};

	const handleDeleteConfirm = () => {
		if (!torrentToDelete) return;
		sendAction({
			action: 'delete_torrent',
			info_hash: torrentToDelete.info_hash,
			delete_files: deleteFilesOption
		});
		setTorrentToDelete(null);
		setDeleteFilesOption(false);
	};

	const fetchTorrentFiles = async (infoHash: string) => {
		setLoadingFiles(true);
		try {
			const res = await fetch(`/api/torrent/files?info_hash=${infoHash}`, {
				headers: { 'Authorization': `Bearer ${token}` }
			});
			if (res.ok) {
				const data = await res.json();
				if (data.status === 'fetching_metadata') {
					showGlobalAlert('Torrent metadata is still fetching from peers. Please wait.', { title: 'Metadata Fetching', variant: 'info' });
					setSelectedTorrent(null);
				} else {
					setTorrentFiles(data || []);
					// Select all files by default initially
					setSelectedFileIndices((data || []).map((f: TorrentFileItem) => f.index));
				}
			}
		} catch (err) {
			console.error(err);
		} finally {
			setLoadingFiles(false);
		}
	};

	const openFilesModal = (torrent: TorrentJob) => {
		setSelectedTorrent(torrent);
		fetchTorrentFiles(torrent.info_hash);
	};

	const handleToggleFileSelection = (idx: number) => {
		setSelectedFileIndices(prev => 
			prev.includes(idx) ? prev.filter(i => i !== idx) : [...prev, idx]
		);
	};

	const handleSaveFilePriorities = async () => {
		if (!selectedTorrent) return;
		try {
			const res = await fetch('/api/torrent/select-files', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({
					info_hash: selectedTorrent.info_hash,
					selected_files: selectedFileIndices
				})
			});
			if (res.ok) {
				setSelectedTorrent(null);
			}
		} catch (err) {
			console.error(err);
		}
	};

	const addedTorrent = torrents.find(t => t.info_hash === addedInfoHash);
	const currentPeers = addedTorrent ? addedTorrent.peers : 0;
	const filteredModalFiles = modalFiles.filter(file => 
		file.path.toLowerCase().includes(fileSearchQuery.toLowerCase())
	);

	return (
		<div style={{ padding: 24, color: 'var(--color-brand-text)' }}>
			{/* Header */}
			<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
				<div>
					<h1 style={{ margin: 0, fontSize: 24, fontWeight: 700, color: 'var(--color-brand-heading)' }}>BitTorrent Client</h1>
					<p style={{ margin: '4px 0 0', fontSize: 13, color: 'var(--color-brand-muted)' }}>Manage high performance parallel torrent downloads</p>
				</div>
				<div style={{ display: 'flex', gap: 12 }}>
					<button 
						onClick={() => setShowAddModal(true)} 
						className="btn btn--primary" 
						style={{ display: 'flex', alignItems: 'center', gap: 8, background: '#ea580c', borderColor: '#ea580c', color: '#fff' }}
					>
						<FiPlus size={16} /> Add Torrent
					</button>
					<button 
						onClick={() => setShowConfigModal(true)} 
						className="btn" 
						style={{ display: 'flex', alignItems: 'center', gap: 8 }}
					>
						<FiSettings size={16} /> Settings
					</button>
				</div>
			</div>

			{/* Torrent Jobs List */}
			{torrents.length === 0 ? (
				<div style={{
					background: 'var(--color-card-bg, #fff)',
					border: '1px solid var(--color-brand-border, rgba(0,0,0,0.08))',
					borderRadius: 12,
					padding: '48px 24px',
					textAlign: 'center',
					color: 'var(--color-brand-muted)'
				}}>
					<FiDownloadCloud size={48} style={{ marginBottom: 16, strokeWidth: 1.5, color: 'var(--color-brand-muted)' }} />
					<p style={{ margin: 0, fontSize: 16, fontWeight: 500 }}>No Active Torrents</p>
					<p style={{ margin: '4px 0 16px', fontSize: 13 }}>Add a magnet link or upload a torrent file to start downloading</p>
					<button onClick={() => setShowAddModal(true)} className="btn btn--sm" style={{ background: '#ea580c', color: '#fff', border: 'none' }}>Get Started</button>
				</div>
			) : (
				<div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
					{torrents.map((t) => {
						const isDone = t.status === 'completed' || t.status === 'seeding';
						const fileMissing = isDone && t.file_exists === false;
						const isHovered = hoveredTorrentHash === t.info_hash;
						const showOverlay = fileMissing && isHovered && !dismissedOverlays[t.info_hash];

						return (
							<div 
								key={t.info_hash} 
								onMouseEnter={() => setHoveredTorrentHash(t.info_hash)}
								onMouseLeave={() => {
									setHoveredTorrentHash(null);
									if (dismissedOverlays[t.info_hash]) {
										setDismissedOverlays(prev => ({ ...prev, [t.info_hash]: false }));
									}
								}}
								style={{
									background: 'var(--color-card-bg, #fff)',
									border: '1px solid var(--color-brand-border, rgba(0,0,0,0.08))',
									borderRadius: 12,
									padding: 20,
									boxShadow: '0 4px 6px -1px rgba(0,0,0,0.05), 0 2px 4px -1px rgba(0,0,0,0.03)',
									position: 'relative',
									...(fileMissing ? {
										filter: 'grayscale(1) contrast(1.1) brightness(0.8)',
										border: '1px dashed #4b5563',
										background: '#121212',
									} : {})
								}}
							>
								{showOverlay && (
									<div className="missing-file-overlay" style={{
										position: 'absolute',
										top: 0,
										left: 0,
										right: 0,
										bottom: 0,
										background: 'rgba(0, 0, 0, 0.85)',
										zIndex: 50,
										display: 'flex',
										alignItems: 'center',
										justifyContent: 'center',
										gap: 12,
										borderRadius: 12,
									}}>
										<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 10 }}>
											<div style={{ color: '#ef4444', fontSize: 13, fontWeight: 'bold' }}>
												File is missing physically on disk
											</div>
											<div style={{ display: 'flex', gap: 12 }}>
												<button 
													className="btn btn--primary" 
													onClick={(e) => {
														e.stopPropagation();
														sendAction({
															action: 'delete_torrent',
															info_hash: t.info_hash,
															delete_files: false
														});
													}}
													style={{ 
														background: '#dc2626', 
														borderColor: '#dc2626',
														color: '#fff',
														padding: '6px 12px',
														fontSize: 12,
														fontWeight: 'bold',
														display: 'flex',
														alignItems: 'center',
														gap: 6
													}}
												>
													<FiTrash2 size={14} /> Delete Database Record
												</button>
												<button 
													className="btn" 
													onClick={(e) => {
														e.stopPropagation();
														setDismissedOverlays(prev => ({ ...prev, [t.info_hash]: true }));
													}}
													style={{ 
														background: '#374151',
														borderColor: '#4b5563',
														color: '#fff',
														padding: '6px 12px',
														fontSize: 12
													}}
												>
													Cancel / View Info
												</button>
											</div>
										</div>
									</div>
								)}
								<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 12, gap: 16 }}>
									<div style={{ minWidth: 0 }}>
										<h3 style={{ margin: 0, fontSize: 16, fontWeight: 600, color: 'var(--color-brand-heading)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
											{t.name}
										</h3>
										<div style={{ display: 'flex', gap: 16, marginTop: 4, fontSize: 12, color: 'var(--color-brand-muted)', flexWrap: 'wrap' }}>
											<span>Hash: <span style={{ fontFamily: 'monospace' }}>{t.info_hash.substring(0, 8)}...</span></span>
											<span>Dir: {t.save_directory}</span>
											{t.total_bytes > 0 && <span>Size: {formatBytes(t.total_bytes)}</span>}
											<span>Peers: {t.peers}</span>
										</div>
									</div>
									
									{/* Actions */}
									<div style={{ display: 'flex', gap: 8, flexShrink: 0 }}>
										<button 
											onClick={() => openFilesModal(t)}
											className="btn btn--sm"
											title="Select Files"
											style={{ padding: '6px 10px', display: 'flex', alignItems: 'center', gap: 4 }}
										>
											<FiLayers size={13} /> Files
										</button>
										
										{t.status === 'paused' ? (
											<button 
												onClick={() => handleResume(t.info_hash)}
												className="btn btn--sm"
												style={{ background: 'rgba(34, 197, 94, 0.1)', color: '#22c55e', borderColor: 'rgba(34, 197, 94, 0.2)' }}
												title="Resume"
											>
												<FiPlay size={13} />
											</button>
										) : (
											<button 
												onClick={() => handlePause(t.info_hash)}
												className="btn btn--sm"
												style={{ background: 'rgba(234, 88, 12, 0.1)', color: '#ea580c', borderColor: 'rgba(234, 88, 12, 0.2)' }}
												title="Pause"
											>
												<FiPause size={13} />
											</button>
										)}

										<button 
											onClick={() => setTorrentToDelete(t)}
											className="btn btn--sm"
											style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#ef4444', borderColor: 'rgba(239, 68, 68, 0.2)' }}
											title="Delete"
										>
											<FiTrash2 size={13} />
										</button>
									</div>
								</div>

								{/* Progress Bar Component */}
								<TorrentProgressBar progress={t.progress} status={t.status} />

								{/* Download status / details footer */}
								<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 8, fontSize: 12 }}>
									<div style={{ display: 'flex', gap: 16 }}>
										<span style={{
											textTransform: 'uppercase',
											fontSize: 10,
											fontWeight: 700,
											padding: '2px 6px',
											borderRadius: 4,
											background: t.status === 'downloading' ? 'rgba(56, 189, 248, 0.1)' : t.status === 'completed' || t.status === 'seeding' ? 'rgba(34, 197, 94, 0.1)' : t.status === 'queued' ? 'rgba(245, 158, 11, 0.15)' : 'rgba(156, 163, 175, 0.1)',
											color: t.status === 'downloading' ? '#0284c7' : t.status === 'completed' || t.status === 'seeding' ? '#16a34a' : t.status === 'queued' ? '#b45309' : '#4b5563'
										}}>
											{t.status === 'queued' ? 'queued · waiting for disk' : t.status}
										</span>
										{t.status === 'downloading' && (
											<>
												<span>DL: <strong style={{ color: '#ea580c' }}>{formatSpeed(t.download_speed)}</strong></span>
												<span>UL: <strong style={{ color: '#3b82f6' }}>{formatSpeed(t.upload_speed)}</strong></span>
											</>
										)}
										{t.status === 'seeding' && (
											<span>UL: <strong style={{ color: '#22c55e' }}>{formatSpeed(t.upload_speed)}</strong></span>
										)}
									</div>
									<div>
										{t.total_bytes > 0 ? (
											<span>{formatBytes(t.downloaded)} of {formatBytes(t.total_bytes)} ({t.progress.toFixed(1)}%)</span>
										) : (
											<span>Connecting to trackers...</span>
										)}
									</div>
								</div>
							</div>
						);
					})}
				</div>
			)}

			{/* ADD TORRENT MODAL */}
			{showAddModal && (
				<div style={{
					position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
					background: 'rgba(0,0,0,0.6)', backdropFilter: 'blur(4px)',
					display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000
				}}>
					<div style={{
						background: 'var(--color-card-bg, #fff)',
						borderRadius: 16, width: '90%', maxWidth: 520, padding: 24,
						border: '1px solid var(--color-brand-border)',
						boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1), 0 10px 10px -5px rgba(0, 0, 0, 0.04)'
					}}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 20 }}>
							<h3 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--color-brand-heading)' }}>Add New Torrent</h3>
							<button onClick={handleCancelAdd} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-brand-muted)' }}><FiX size={18} /></button>
						</div>

						{addStep === 'input' && (
							<form onSubmit={handleAddTorrent}>
								{/* Magnet Link */}
								<div style={{ marginBottom: 16 }}>
									<label style={{ display: 'block', fontSize: 12, fontWeight: 600, marginBottom: 6, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Magnet Link URI(s) (One link per line)</label>
									<div style={{ position: 'relative' }}>
										<FiLink size={16} style={{ position: 'absolute', left: 12, top: 12, color: 'var(--color-brand-muted)' }} />
										<textarea 
											placeholder="magnet:?xt=urn:btih:...&#10;magnet:?xt=urn:btih:..." 
											value={magnetUri} 
											onChange={(e) => { setMagnetUri(e.target.value); setTorrentFile(null); }}
											rows={3}
											style={{ width: '100%', padding: '10px 12px 10px 38px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', resize: 'vertical', fontFamily: 'monospace', fontSize: 12 }}
										/>
									</div>
								</div>

								{magnetsCount > 1 && (
									<div style={{ 
										background: 'rgba(245,158,11,0.1)', 
										border: '1px solid rgba(245,158,11,0.2)', 
										borderRadius: 8, 
										padding: '8px 12px', 
										marginBottom: 16,
										fontSize: 11,
										color: '#f59e0b',
										display: 'flex',
										alignItems: 'center',
										gap: 6
									}}>
										<FiInfo size={14} /> Bulk download: File selection will be skipped and default settings will be used.
									</div>
								)}

								<div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', margin: '12px 0', fontSize: 12, color: 'var(--color-brand-muted)', fontWeight: 500 }}>
									<span>— OR —</span>
								</div>

								{/* Torrent File Drag & Drop */}
								<div style={{ marginBottom: 20 }}>
									<label style={{ display: 'block', fontSize: 12, fontWeight: 600, marginBottom: 8, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Torrent Metadata File</label>
									<div 
										onClick={() => document.getElementById('torrent-file-input')?.click()}
										onDragOver={(e) => e.preventDefault()}
										onDrop={(e) => {
											e.preventDefault();
											const file = e.dataTransfer.files?.[0];
											if (file && file.name.endsWith('.torrent')) {
												setTorrentFile(file);
												setMagnetUri('');
											}
										}}
										style={{
											border: '2px dashed var(--color-brand-border)',
											borderRadius: 12,
											padding: '24px 16px',
											textAlign: 'center',
											cursor: 'pointer',
											background: torrentFile ? 'rgba(234, 88, 12, 0.05)' : 'rgba(0,0,0,0.18)',
											borderColor: torrentFile ? '#ea580c' : 'var(--color-brand-border)',
											transition: 'all 0.2s ease',
											display: 'flex',
											flexDirection: 'column',
											alignItems: 'center',
											justifyContent: 'center',
											gap: 8
										}}
									>
										<input 
											id="torrent-file-input"
											type="file" 
											accept=".torrent"
											onChange={(e) => { if (e.target.files?.[0]) { setTorrentFile(e.target.files[0]); setMagnetUri(''); } }}
											style={{ display: 'none' }}
										/>
										<FiDownloadCloud size={28} style={{ color: torrentFile ? '#ea580c' : 'var(--color-brand-muted)' }} />
										<span style={{ fontSize: 13, fontWeight: 500, color: 'var(--color-brand-heading)' }}>
											{torrentFile ? torrentFile.name : 'Drag & Drop .torrent file here or click to browse'}
										</span>
										{torrentFile && (
											<span style={{ fontSize: 11, color: 'var(--color-brand-muted)' }}>
												{(torrentFile.size / 1024).toFixed(1)} KB • Click to change
											</span>
										)}
									</div>
								</div>

								{/* Save Directory Picker */}
								<div style={{ marginBottom: 16 }}>
									<label style={{ display: 'block', fontSize: 12, fontWeight: 600, marginBottom: 6, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Save Directory</label>
									<div style={{ display: 'flex', gap: 8 }}>
										<input 
											type="text" 
											value={saveDir} 
											onChange={(e) => setSaveDir(e.target.value)}
											style={{ flex: 1, padding: '10px 12px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', fontSize: 13 }}
										/>
										<button type="button" onClick={() => openFolderPicker('add')} className="btn" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
											<FiFolder size={14} /> Browse
										</button>
									</div>
								</div>

								{/* Beautiful Switch to toggle select files */}
								<div style={{ 
									display: 'flex', 
									alignItems: 'center', 
									justifyContent: 'space-between', 
									background: 'rgba(0,0,0,0.18)', 
									padding: '12px 16px', 
									borderRadius: 12, 
									border: '1px solid var(--color-brand-border)',
									marginBottom: 24,
									opacity: magnetsCount > 1 ? 0.5 : 1,
									pointerEvents: magnetsCount > 1 ? 'none' : 'auto'
								}}>
									<div>
										<span style={{ display: 'block', fontSize: 13, fontWeight: 600, color: 'var(--color-brand-heading)' }}>Select files to download</span>
										<span style={{ display: 'block', fontSize: 11, color: 'var(--color-brand-muted)', marginTop: 2 }}>
											{magnetsCount > 1 ? 'Disabled for multiple magnet links' : 'Fetch metadata first to check/uncheck files'}
										</span>
									</div>
									<label className="switch" style={{ position: 'relative', display: 'inline-block', width: 44, height: 22 }}>
										<input 
											type="checkbox" 
											disabled={magnetsCount > 1}
											checked={magnetsCount > 1 ? false : selectFilesEnabled} 
											onChange={(e) => setSelectFilesEnabled(e.target.checked)} 
											style={{ opacity: 0, width: 0, height: 0 }} 
										/>
										<span style={{ 
											position: 'absolute', cursor: 'pointer', top: 0, left: 0, right: 0, bottom: 0, 
											backgroundColor: (magnetsCount > 1 ? false : selectFilesEnabled) ? '#ea580c' : 'rgba(255,255,255,0.1)', 
											transition: '.3s', borderRadius: 22 
										}}>
											<span style={{ 
												position: 'absolute', content: '""', height: 16, width: 16, left: 3, bottom: 3, 
												backgroundColor: 'white', transition: '.3s', borderRadius: '50%',
												transform: (magnetsCount > 1 ? false : selectFilesEnabled) ? 'translateX(22px)' : 'translateX(0)' 
											}} />
										</span>
									</label>
								</div>

								<div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
									<button type="button" onClick={handleCancelAdd} className="btn">Cancel</button>
									<button type="submit" className="btn btn--primary" style={{ background: '#ea580c', borderColor: '#ea580c', color: '#fff', display: 'flex', alignItems: 'center', gap: 8 }} disabled={!magnetUri.trim() && !torrentFile}>
										{selectFilesEnabled && magnetsCount <= 1 ? (
											<>Next <FiChevronLeft size={16} style={{ transform: 'rotate(180deg)' }} /></>
										) : (
											'Start Downloading'
										)}
									</button>
								</div>
							</form>
						)}

						{addStep === 'submitting' && (
							<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 16, padding: '30px 10px', textAlign: 'center' }}>
								<div className="spinner" style={{ width: 40, height: 40, border: '3px solid rgba(234,88,12,0.1)', borderTopColor: '#ea580c', borderRadius: '50%' }} />
								<div>
									<h4 style={{ fontSize: 15, fontWeight: 700, margin: 0, color: 'var(--color-brand-heading)' }}>Submitting to Server</h4>
									<p style={{ fontSize: 12, color: 'var(--color-brand-muted)', marginTop: 6 }}>
										{magnetsCount > 1 
											? `Registering torrent jobs...` 
											: 'Uploading torrent payload and registering job metadata...'
										}
									</p>
								</div>
							</div>
						)}

						{addStep === 'fetching_metadata' && (
							<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 16, padding: '30px 10px', textAlign: 'center' }}>
								<div className="spinner" style={{ width: 40, height: 40, border: '3px solid rgba(59,130,246,0.1)', borderTopColor: '#3b82f6', borderRadius: '50%' }} />
								<div>
									<h4 style={{ fontSize: 15, fontWeight: 700, margin: 0, color: 'var(--color-brand-heading)' }}>Retrieving Torrent Info</h4>
									<p style={{ fontSize: 12, color: 'var(--color-brand-muted)', marginTop: 6 }}>
										{magnetUri ? 'Downloading magnet metadata from BitTorrent network peers...' : 'Parsing metainfo tree...'}
									</p>
									<div style={{ display: 'inline-block', marginTop: 12, fontSize: 11, background: 'rgba(0,0,0,0.18)', padding: '4px 10px', borderRadius: 20, border: '1px solid var(--color-brand-border)', fontFamily: 'monospace' }}>
										Peers connected: {currentPeers}
									</div>
								</div>
								<div style={{ display: 'flex', gap: 12, marginTop: 16, width: '100%' }}>
									<button type="button" className="btn" style={{ flex: 1 }} onClick={handleCancelAdd}>Delete & Cancel</button>
									<button type="button" className="btn btn--primary" style={{ flex: 1 }} onClick={handleCloseKeepBackground}>Run in Background</button>
								</div>
							</div>
						)}

						{addStep === 'select_files' && (
							<div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
								<div>
									<h4 style={{ fontSize: 14, fontWeight: 700, margin: 0, color: 'var(--color-brand-heading)' }}>Select Files to Download</h4>
									<p style={{ fontSize: 11, color: 'var(--color-brand-muted)', marginTop: 2 }}>Uncheck any files you do not wish to download onto the server.</p>
								</div>

								{/* Search / Filter input */}
								<input 
									type="text" 
									placeholder="Search files by name..." 
									value={fileSearchQuery}
									onChange={(e) => setFileSearchQuery(e.target.value)}
									style={{ width: '100%', padding: '8px 12px', fontSize: 12, borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', color: 'var(--color-brand-heading)', outline: 'none' }}
								/>

								{/* Select Action buttons */}
								<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
									<div style={{ display: 'flex', gap: 8 }}>
										<button type="button" className="btn btn--sm" style={{ padding: '2px 8px', fontSize: 11 }} onClick={handleSelectAllFiles}>Select All</button>
										<button type="button" className="btn btn--sm" style={{ padding: '2px 8px', fontSize: 11 }} onClick={handleDeselectAllFiles}>Deselect All</button>
									</div>
									<span style={{ fontSize: 11, color: 'var(--color-brand-muted)' }}>
										Selected {selectedModalFileIndices.length} of {modalFiles.length} files
									</span>
								</div>

								{/* Scrollable file list */}
								<div style={{ border: '1px solid var(--color-brand-border)', borderRadius: 8, background: 'var(--color-brand-bg)', maxHeight: 200, overflowY: 'auto', padding: '4px 0' }}>
									{filteredModalFiles.length === 0 ? (
										<div style={{ padding: 20, textAlign: 'center', color: 'var(--color-brand-muted)', fontSize: 12 }}>No matching files found</div>
									) : (
										filteredModalFiles.map(file => {
											const isChecked = selectedModalFileIndices.includes(file.index);
											return (
												<label 
													key={file.index} 
													style={{ 
														display: 'flex', 
														alignItems: 'center', 
														gap: 10, 
														padding: '8px 12px', 
														borderBottom: '1px solid rgba(255,255,255,0.03)', 
														cursor: 'pointer',
														userSelect: 'none',
														background: isChecked ? 'rgba(255,255,255,0.01)' : 'transparent'
													}}
												>
													<input 
														type="checkbox" 
														checked={isChecked}
														onChange={() => handleToggleModalFile(file.index)}
														style={{ accentColor: 'var(--color-brand)' }}
													/>
													<div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
														<span style={{ fontSize: 12, fontWeight: 500, color: 'var(--color-brand-heading)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
															{file.path}
														</span>
														<span style={{ fontSize: 10, color: 'var(--color-brand-muted)' }}>
															{formatBytes(file.length)}
														</span>
													</div>
												</label>
											);
										})
									)}
								</div>

								{/* Save directory input (again, editable) */}
								<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Save Location</label>
									<div style={{ display: 'flex', gap: 8 }}>
										<input 
											type="text" 
											value={saveDir} 
											onChange={(e) => setSaveDir(e.target.value)}
											style={{ flex: 1, padding: '8px 12px', fontSize: 12, borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', outline: 'none' }}
										/>
										<button type="button" className="btn btn--sm" style={{ padding: '0 12px' }} onClick={() => openFolderPicker('add')}><FiFolder size={14} /></button>
									</div>
								</div>

								<div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12, marginTop: 8 }}>
									<button type="button" className="btn" onClick={handleCancelAdd}>Cancel</button>
									<button type="button" className="btn btn--primary" style={{ background: '#ea580c', borderColor: '#ea580c', color: '#fff' }} onClick={handleConfirmDownload}>Confirm & Download</button>
								</div>
							</div>
						)}
					</div>
				</div>
			)}

			{/* FOLDER PICKER MODAL */}
			{showFolderModal && (
				<div style={{
					position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
					background: 'rgba(0,0,0,0.6)', backdropFilter: 'blur(4px)',
					display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1100
				}}>
					<div style={{
						background: 'var(--color-card-bg, #fff)',
						borderRadius: 16, width: '90%', maxWidth: 450, padding: 20,
						border: '1px solid var(--color-brand-border)',
						boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1)'
					}}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
							<h3 style={{ margin: 0, fontSize: 16, fontWeight: 700, color: 'var(--color-brand-heading)' }}>Select Destination Folder</h3>
							<button onClick={() => setShowFolderModal(false)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-brand-muted)' }}><FiX size={16} /></button>
						</div>

						{/* Directory Path */}
						<div style={{ display: 'flex', alignItems: 'center', gap: 8, background: 'var(--color-brand-bg, rgba(0,0,0,0.03))', padding: '8px 12px', borderRadius: 8, marginBottom: 16, fontSize: 13 }}>
							<FiServer size={14} style={{ color: 'var(--color-brand-muted)' }} />
							<span style={{ fontFamily: 'monospace', wordBreak: 'break-all' }}>{currentPath}</span>
						</div>

						{/* Folders List */}
						<div style={{ border: '1px solid var(--color-brand-border)', borderRadius: 8, height: 200, overflowY: 'auto', marginBottom: 16 }}>
							{currentPath !== '/' && currentPath !== '' && (
								<div 
									onClick={handleDirUp} 
									style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px', borderBottom: '1px solid var(--color-brand-border)', cursor: 'pointer', fontSize: 13, background: 'rgba(0,0,0,0.01)' }}
								>
									<FiChevronLeft size={16} style={{ color: 'var(--color-brand-muted)' }} />
									<span style={{ fontWeight: 500 }}>.. (Up one level)</span>
								</div>
							)}

							{directories.length === 0 ? (
								<div style={{ padding: '32px 16px', textAlign: 'center', color: 'var(--color-brand-muted)', fontSize: 13 }}>
									No subfolders inside this directory
								</div>
							) : (
								directories.map((dir) => (
									<div 
										key={dir} 
										onClick={() => handleDirSelect(dir)} 
										style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px', borderBottom: '1px solid var(--color-brand-border)', cursor: 'pointer', fontSize: 13 }}
									>
										<FiFolder size={14} style={{ color: '#ea580c' }} />
										<span>{dir}</span>
									</div>
								))
							)}
						</div>

						{/* Create New Folder Widget */}
						{showNewFolderInput ? (
							<div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
								<input 
									type="text" 
									placeholder="New folder name..." 
									value={newFolderName}
									onChange={(e) => setNewFolderName(e.target.value)}
									style={{ flex: 1, padding: '8px 12px', border: '1px solid var(--color-brand-border)', borderRadius: 8, fontSize: 13, background: 'transparent', color: 'inherit' }}
								/>
								<button onClick={handleCreateDirectory} className="btn" style={{ background: '#ea580c', color: '#fff', border: 'none' }}><FiCheck size={14} /></button>
								<button onClick={() => setShowNewFolderInput(false)} className="btn"><FiX size={14} /></button>
							</div>
						) : (
							<button 
								onClick={() => setShowNewFolderInput(true)} 
								className="btn" 
								style={{ display: 'flex', alignItems: 'center', gap: 6, width: '100%', justifyContent: 'center', marginBottom: 16 }}
							>
								<FiFolderPlus size={14} /> Create New Subfolder
							</button>
						)}

						<div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
							<button type="button" onClick={() => setShowFolderModal(false)} className="btn">Cancel</button>
							<button 
								type="button" 
								onClick={() => {
									if (folderPickerTarget === 'add') {
										setSaveDir(currentPath);
									} else {
										setConfig(prev => ({ ...prev, save_directory: currentPath }));
									}
									setShowFolderModal(false);
								}} 
								className="btn btn--primary"
								style={{ background: '#ea580c', borderColor: '#ea580c', color: '#fff' }}
							>
								Choose Directory
							</button>
						</div>
					</div>
				</div>
			)}

			{/* DELETE CONFIRMATION MODAL */}
			{torrentToDelete && (
				<div style={{
					position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
					background: 'rgba(0,0,0,0.6)', backdropFilter: 'blur(4px)',
					display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1200
				}}>
					<div style={{
						background: 'var(--color-card-bg, #fff)',
						borderRadius: 16, width: '90%', maxWidth: 450, padding: 24,
						border: '1px solid var(--color-brand-border)',
						boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1)'
					}}>
						<h3 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--color-brand-heading)', marginBottom: 12 }}>Remove Torrent Job</h3>
						<p style={{ margin: 0, fontSize: 14, color: 'var(--color-brand-muted)', marginBottom: 20 }}>
							Are you sure you want to remove <strong style={{ color: 'var(--color-brand-heading)' }}>{torrentToDelete.name}</strong>?
						</p>

						<div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 24 }}>
							<input 
								type="checkbox" 
								id="deleteFilesOpt"
								checked={deleteFilesOption}
								onChange={(e) => setDeleteFilesOption(e.target.checked)}
								style={{ width: 16, height: 16, cursor: 'pointer' }}
							/>
							<label htmlFor="deleteFilesOpt" style={{ fontSize: 13, fontWeight: 500, cursor: 'pointer' }}>
								Also delete downloaded data from disk
							</label>
						</div>

						<div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
							<button onClick={() => setTorrentToDelete(null)} className="btn">Cancel</button>
							<button onClick={handleDeleteConfirm} className="btn btn--primary" style={{ background: '#ef4444', borderColor: '#ef4444', color: '#fff' }}>Remove</button>
						</div>
					</div>
				</div>
			)}

			{/* SELECT TORRENT FILES MODAL */}
			{selectedTorrent && (
				<div style={{
					position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
					background: 'rgba(0,0,0,0.6)', backdropFilter: 'blur(4px)',
					display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1200
				}}>
					<div style={{
						background: 'var(--color-card-bg, #fff)',
						borderRadius: 16, width: '90%', maxWidth: 650, padding: 24,
						border: '1px solid var(--color-brand-border)',
						boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1)',
						maxHeight: '90vh', display: 'flex', flexDirection: 'column'
					}}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
							<h3 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--color-brand-heading)' }}>Select Torrent Files</h3>
							<button onClick={() => setSelectedTorrent(null)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-brand-muted)' }}><FiX size={18} /></button>
						</div>
						<p style={{ margin: '0 0 16px', fontSize: 13, color: 'var(--color-brand-muted)' }}>Choose which files to download inside the package</p>

						{loadingFiles ? (
							<div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 48 }}>
								<div style={{ width: 28, height: 28, border: '3px solid rgba(0,0,0,0.08)', borderTopColor: '#ea580c', borderRadius: '50%', animation: 'spin .6s linear infinite' }} />
							</div>
						) : (
							<div style={{ flex: 1, overflowY: 'auto', border: '1px solid var(--color-brand-border)', borderRadius: 8, marginBottom: 20 }}>
								{torrentFiles.map((file) => (
									<div 
										key={file.index} 
										style={{ 
											display: 'flex', 
											alignItems: 'center', 
											justifyContent: 'space-between', 
											padding: '12px 16px', 
											borderBottom: '1px solid var(--color-brand-border)',
											background: selectedFileIndices.includes(file.index) ? 'rgba(234, 88, 12, 0.02)' : 'transparent'
										}}
									>
										<div style={{ display: 'flex', alignItems: 'center', gap: 12, minWidth: 0, flex: 1 }}>
											<input 
												type="checkbox" 
												checked={selectedFileIndices.includes(file.index)}
												onChange={() => handleToggleFileSelection(file.index)}
												style={{ width: 16, height: 16, cursor: 'pointer', accentColor: '#ea580c' }}
											/>
											<div style={{ minWidth: 0 }}>
												<span style={{ fontSize: 13, fontWeight: 500, display: 'block', wordBreak: 'break-all' }}>{file.path}</span>
												<span style={{ fontSize: 11, color: 'var(--color-brand-muted)' }}>
													{formatBytes(file.completed)} completed of {formatBytes(file.length)} ({file.percentage.toFixed(1)}%)
												</span>
											</div>
										</div>
									</div>
								))}
							</div>
						)}

						<div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12 }}>
							<button onClick={() => setSelectedTorrent(null)} className="btn">Cancel</button>
							<button 
								onClick={handleSaveFilePriorities} 
								className="btn btn--primary" 
								style={{ background: '#ea580c', borderColor: '#ea580c', color: '#fff' }}
								disabled={loadingFiles}
							>
								Save Changes
							</button>
						</div>
					</div>
				</div>
			)}

			{/* SETTINGS MODAL */}
			{showConfigModal && (
				<div style={{
					position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
					background: 'rgba(0,0,0,0.6)', backdropFilter: 'blur(4px)',
					display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000
				}}>
					<div style={{
						background: 'var(--color-card-bg, #fff)',
						borderRadius: 16, width: '90%', maxWidth: 650, padding: 24,
						border: '1px solid var(--color-brand-border)',
						boxShadow: '0 20px 25px -5px rgba(0, 0, 0, 0.1), 0 10px 10px -5px rgba(0, 0, 0, 0.04)',
						maxHeight: '90vh', overflowY: 'auto'
					}}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 20 }}>
							<h3 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--color-brand-heading)' }}>BitTorrent Client Configuration</h3>
							<button onClick={() => setShowConfigModal(false)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-brand-muted)' }}><FiX size={18} /></button>
						</div>

						<div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
							{/* Form Grid */}
							<div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
								{/* Left Column */}
								<div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
									<div>
										<label style={{ display: 'block', fontSize: 11, fontWeight: 700, marginBottom: 4, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Default Save Folder</label>
										<div style={{ display: 'flex', gap: 8 }}>
											<input 
												type="text" 
												value={config.save_directory} 
												onChange={(e) => setConfig(prev => ({ ...prev, save_directory: e.target.value }))}
												style={{ flex: 1, padding: '8px 10px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', fontSize: 13 }}
											/>
											<button type="button" onClick={() => openFolderPicker('settings')} className="btn btn--sm" style={{ display: 'flex', alignItems: 'center', gap: 4 }}><FiFolder size={12} /> Browse</button>
										</div>
									</div>

									<div>
										<label style={{ display: 'block', fontSize: 11, fontWeight: 700, marginBottom: 4, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Upload Speed Limit (MB/s)</label>
										<input 
											type="number" 
											step="0.1"
											min="0"
											value={config.upload_limit_mb} 
											onChange={(e) => setConfig(prev => ({ ...prev, upload_limit_mb: parseFloat(e.target.value) || 0 }))}
											style={{ width: '100%', padding: '8px 10px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', fontSize: 13 }}
											placeholder="0 for unlimited"
										/>
									</div>

									<div>
										<label style={{ display: 'block', fontSize: 11, fontWeight: 700, marginBottom: 4, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Download Speed Limit (MB/s)</label>
										<input 
											type="number" 
											step="0.1"
											min="0"
											value={config.download_limit_mb} 
											onChange={(e) => setConfig(prev => ({ ...prev, download_limit_mb: parseFloat(e.target.value) || 0 }))}
											style={{ width: '100%', padding: '8px 10px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', fontSize: 13 }}
											placeholder="0 for unlimited"
										/>
									</div>

									<div>
										<label style={{ display: 'block', fontSize: 11, fontWeight: 700, marginBottom: 4, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Piece Hashers Per Torrent</label>
										<input 
											type="number" 
											min="1"
											max="16"
											value={config.piece_hashers_per_torrent} 
											onChange={(e) => setConfig(prev => ({ ...prev, piece_hashers_per_torrent: parseInt(e.target.value) || 4 }))}
											style={{ width: '100%', padding: '8px 10px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', fontSize: 13 }}
										/>
									</div>
								</div>

								{/* Right Column */}
								<div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
									<div>
										<label style={{ display: 'block', fontSize: 11, fontWeight: 700, marginBottom: 4, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Max Connections Per Torrent</label>
										<input 
											type="number" 
											min="10"
											max="2000"
											value={config.max_connections_per_torrent} 
											onChange={(e) => setConfig(prev => ({ ...prev, max_connections_per_torrent: parseInt(e.target.value) || 200 }))}
											style={{ width: '100%', padding: '8px 10px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', fontSize: 13 }}
										/>
									</div>

									<div>
										<label style={{ display: 'block', fontSize: 11, fontWeight: 700, marginBottom: 4, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Max Half-Open Connections</label>
										<input 
											type="number" 
											min="5"
											max="500"
											value={config.max_half_open_connections} 
											onChange={(e) => setConfig(prev => ({ ...prev, max_half_open_connections: parseInt(e.target.value) || 100 }))}
											style={{ width: '100%', padding: '8px 10px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', fontSize: 13 }}
										/>
									</div>

									{/* Toggles */}
									<div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginTop: 8 }}>
										<label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
											<input 
												type="checkbox" 
												checked={config.enable_dht} 
												onChange={(e) => setConfig(prev => ({ ...prev, enable_dht: e.target.checked }))}
												style={{ cursor: 'pointer', accentColor: '#ea580c' }}
											/>
											<span>Enable DHT (Distributed Hash Table)</span>
										</label>

										<label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
											<input 
												type="checkbox" 
												checked={config.enable_pex} 
												onChange={(e) => setConfig(prev => ({ ...prev, enable_pex: e.target.checked }))}
												style={{ cursor: 'pointer', accentColor: '#ea580c' }}
											/>
											<span>Enable PEX (Peer Exchange)</span>
										</label>

										<label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
											<input 
												type="checkbox" 
												checked={config.enable_utp} 
												onChange={(e) => setConfig(prev => ({ ...prev, enable_utp: e.target.checked }))}
												style={{ cursor: 'pointer', accentColor: '#ea580c' }}
											/>
											<span>Enable uTP Protocol</span>
										</label>

										<label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
											<input 
												type="checkbox" 
												checked={config.enable_tcp} 
												onChange={(e) => setConfig(prev => ({ ...prev, enable_tcp: e.target.checked }))}
												style={{ cursor: 'pointer', accentColor: '#ea580c' }}
											/>
											<span>Enable TCP Protocol</span>
										</label>

										<label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
											<input 
												type="checkbox" 
												checked={config.enable_upload} 
												onChange={(e) => setConfig(prev => ({ ...prev, enable_upload: e.target.checked }))}
												style={{ cursor: 'pointer', accentColor: '#ea580c' }}
											/>
											<span>Enable Seeding / Uploading</span>
										</label>
									</div>
								</div>
							</div>

							{/* Custom Trackers list */}
							<div>
								<label style={{ display: 'block', fontSize: 11, fontWeight: 700, marginBottom: 4, textTransform: 'uppercase', letterSpacing: 0.5, color: 'var(--color-brand-muted)' }}>Inject Custom Trackers (Newline Separated)</label>
								<textarea 
									rows={4}
									value={config.custom_trackers}
									onChange={(e) => setConfig(prev => ({ ...prev, custom_trackers: e.target.value }))}
									style={{ width: '100%', padding: '8px 10px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'transparent', color: 'inherit', fontSize: 12, fontFamily: 'monospace' }}
									placeholder="udp://tracker.opentrackr.org:1337/announce"
								/>
							</div>
						</div>

						<div style={{ display: 'flex', justifyContent: 'flex-end', gap: 12, marginTop: 20 }}>
							<button onClick={() => setShowConfigModal(false)} className="btn">Cancel</button>
							<button 
								onClick={handleSaveConfig} 
								className="btn btn--primary" 
								style={{ background: '#ea580c', borderColor: '#ea580c', color: '#fff' }}
							>
								Save Settings
							</button>
						</div>
					</div>
				</div>
			)}
		</div>
	);
};
